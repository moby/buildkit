/*
 * Copyright (c) 2025 Andrew G. Morgan <morgan@kernel.org>
 *
 * This file contains the low level magic to ensure that the PSX
 * mechanism is the first to intercept the specified psx_sig signal.
 * This is a hidden function of the psx library in its own file
 * because its use of kernel headers conflicts badly with the more
 * traditional *libc provided headers.
 */

#ifndef _GNU_SOURCE
#define _GNU_SOURCE
#endif

#include <signal.h>
#include <string.h>
#include <sys/syscall.h>
#include <sys/wait.h>
#include <unistd.h>

#include "psx_syscall.h"
#include "libpsx.h"

/*
 * This is terrible, but some of the OS installed <asm/signal.h>
 * headers only seem to know about the legacy 32-bit signal masks. So,
 * we need to #define our way around that...
 *
 * Also, the rt_sig*() system calls use a different sigaction
 * definition in these cases, so #define around that too.
 *
 * Coupled with the glibc suppression of access to the exact signal we
 * want to use, we have ended up just inlining all architecture
 * support here.
 *
 * To figure out all this stuff, I started with the following in the
 * linux source tree:
 *
 *  mkdir -p junk/uapi
 *  ln -s ../include/asm-generic ./junk/asm
 *  ln -s ../../include/uapi/asm-generic ./junk/uapi/asm
 *  a=arm ; gcc -I ./arch/$a/include -I ./arch/$a/include/generated \
 *    -I ./include -I ./tools/include/generated/ -I ./junk \
 *    -E ./include/uapi/linux/signal.h | less -psigaction
 */
#if defined(__x86_64__) || defined(__i386__)	   \
    || defined(__arm__) || defined(__aarch64__)	   \
    || defined(__mips__) || defined(__loongarch__) \
    || defined(__powerpc__) || defined(__s390__) || defined(__riscv) \
    || defined(__alpha__) || defined(__hppa__) || defined(__sh__) \
    || defined(__m68k__) || defined(__sparc__)

#undef _NSIG
#undef _NSIG_BPW
#undef _NSIG_WORDS
#undef SA_RESTORER
#undef _HAS_SA_RESTORER
#undef sa_handler
#undef sa_sigaction

#if defined(__mips__)
#define _NSIG        128
#else
#define _NSIG        64
#endif

#define _NSIG_BPW    (8*sizeof(unsigned long))
#define _NSIG_WORDS  (_NSIG / _NSIG_BPW)

#if defined(__x86_64__) || defined(__i386__) \
    || defined(__arm__) \
    || defined(__powerpc__)
/* field used */
#define SA_RESTORER  0x04000000
#endif /* architectures that use SA_RESTORER */

#if defined(SA_RESTORER) \
    || defined(__aarch64__) \
    || defined(__m68k__) || defined(__sh__) || defined(__sparc__) \
    || defined(__s390__) || defined(__sparc__)
/* field defined */
#define _HAS_SA_RESTORER   void *sa_restorer;
#else
#define _HAS_SA_RESTORER
#endif /* architectures that include sa_restorer field */

typedef struct {
    unsigned long sig[_NSIG_WORDS];
} psx_sigset_t;

#define sigset_t psx_sigset_t

struct psx_sigaction {
#if defined(__m68k__) || defined(__alpha__)
    void *sa_handler;
    sigset_t sa_mask;
    unsigned long sa_flags;
    _HAS_SA_RESTORER
#else /* ndef (__m68k__ or __alpha__) */
#if defined(__mips__)
    unsigned long sa_flags;
    void *sa_handler;
#else
    void *sa_handler;
    unsigned long sa_flags;
#endif
    _HAS_SA_RESTORER
    sigset_t sa_mask;
#endif /* def (__m68k__ or __alpha__) */
};

#define sigaction psx_sigaction

#endif /* various architecture defines */

/*
 * Local definition of the "sigaction"s for signal handling including
 * chaining of signal handlers. The content of this type is not known
 * outside of this present source file. The reason for this
 * obfuscation is the fact that this present file uses kernel headers
 * which may differ in structure definitions from the regular *libc
 * implementations with the same name.
 */
typedef struct {
    struct sigaction sig_action;
    struct sigaction chained_action;
} psx_actions_t;

/*
 * Used to allocate space for the actions in the tracker, without
 * revealing the content outside this current file.
 */
__attribute__((visibility ("hidden"))) int psx_actions_size(void) {
    return sizeof(psx_actions_t);
}

/* int how, sigset_t *nset, sigset_t *oset */
#define _psx_rt_sigprocmask(how, nset, oset) \
    syscall(SYS_rt_sigprocmask, how, nset, oset, sizeof(sigset_t))

/* int sig, struct sigaction *act, struct sigaction *oact */
#define _psx_rt_sigaction(sig, act, oact)       \
    syscall(SYS_rt_sigaction, sig, act, oact, sizeof(sigset_t))

#define _psx_sigemptyset(pmask) \
    memset(pmask, 0, sizeof(*(pmask)))

#ifdef _NSIG_WORDS

#define _psx_sigaddset(pmask, signo) \
    (pmask)->sig[(signo-1)/_NSIG_BPW] |= 1UL << (((signo-1)%_NSIG_BPW))

#else /* ndef _NSIG_WORDS */

#define _psx_sigaddset(pmask, signo) \
    *(pmask) |= 1UL << (signo-1)

#endif /* def _NSIG_WORDS */

#ifdef SA_RESTORER
/*
 * Actual assembly code for this "function" is embedded in
 * hidden form in psx_posix_syscall_actor() below.
 */
__attribute__((visibility ("hidden"))) void psx_restorer(void);
#endif /* def SA_RESTORER */

/*
 * psx_posix_syscall_actor performs the system call on the targeted
 * thread and signals it is no longer pending.
 */
static void psx_posix_syscall_actor(int signum, siginfo_t *info, void *ignore) {
    /* bail early to the next in the chain if not something we recognize */
    psx_lock();
    if (signum != psx_tracker.psx_sig || !psx_tracker.cmd.active ||
	info == NULL || info->si_code != SI_TKILL ||
	info->si_pid != psx_tracker.pid) {
	psx_actions_t *actions;
	void (*chained_actor)(int, siginfo_t *,void *);
	psx_unlock();
	actions = psx_tracker.actions;
	chained_actor = (void *) actions->chained_action.sa_handler;
	if (chained_actor != 0) {
	    chained_actor(signum, info, ignore);
	}
#ifdef SA_RESTORER
	/*
	 * This architecture requires we provide a signal exit fixup
	 * trampoline. We use some assembly to provide this, aliased
	 * to a function (prototype above). This assembly won't be
	 * executed as part of the preceding code, but has to live
	 * somewhere so we hide it here where it will get compiled,
	 * but not slow down the PSX functionality.
	 */
	if (psx_tracker.force_failure) {
#if defined(__x86_64__)
	    __asm__ __volatile__("\npsx_restorer:\n\tmov $15,%rax\n\tsyscall\n");
#elif defined(__i386__)
	    __asm__ __volatile__("\npsx_restorer:\n\tmov $173, %eax\n\tint $0x80\n");
#elif defined(__arm__)
	    __asm__ __volatile__("\npsx_restorer:\n\tmov r7,#173\n\tswi 0\n");
#elif defined(__powerpc__)
	    __asm__ __volatile__("\npsx_restorer:\n\tli 0, 172\n\tsc\n");
#else
#error "unsupported architecture - https://bugzilla.kernel.org/show_bug.cgi?id=219687"
#endif /* supported architectures */
	}
#endif /* def SA_RESTORER */
	return;
    }
    psx_unlock();

    long int retval;
    if (!psx_tracker.cmd.six) {
	retval = syscall(psx_tracker.cmd.syscall_nr,
			 psx_tracker.cmd.arg1,
			 psx_tracker.cmd.arg2,
			 psx_tracker.cmd.arg3);
    } else {
	retval = syscall(psx_tracker.cmd.syscall_nr,
			 psx_tracker.cmd.arg1,
			 psx_tracker.cmd.arg2,
			 psx_tracker.cmd.arg3,
			 psx_tracker.cmd.arg4,
			 psx_tracker.cmd.arg5,
			 psx_tracker.cmd.arg6);
    }

    /*
     * communicate the result of the thread's attempt to perform the
     * syscall.
     */
    long tid = _psx_gettid();
    psx_lock();
    psx_thread_ref_t *ref =
	&psx_tracker.map[psx_mix(tid) & psx_tracker.map_mask];
    ref->retval = retval;
    ref->pending = 0;
    /*
     * Block this thread until all threads have been interrupted.
     * This prevents threads clone()ing after running the syscall and
     * confusing the psx mechanism into thinking those new threads
     * need to also run the syscall. They wouldn't need to run it,
     * because they would inherit the thread state of a syscall that
     * has already happened. However, figuring that out for an
     * unblocked thread is hard, so we prevent it from happening.
     */
    while (psx_tracker.cmd.active) {
	psx_cond_wait();
    }
    psx_tracker.incomplete--;
    psx_unlock();
}

/*
 * only used by psx_confirm_sigaction() which is called under lock. We
 * don't have to rely on the stack for this structure because we're
 * reserving static space for it.
 */
static struct sigaction existing_sa;

/*
 * psx_confirm_sigaction (re)confirms that the psx handler is the
 * first handler to respond to the psx signal. It assumes that
 * psx_tracker.psx_sig has been set.
 */
__attribute__((visibility ("hidden"))) void psx_confirm_sigaction(void) {
    sigset_t mask, orig;
    psx_actions_t *actions = psx_tracker.actions;

    /*
     * Block interrupts while potentially rewriting the handler.
     */
    _psx_sigemptyset(&mask);
    _psx_sigaddset(&mask, psx_tracker.psx_sig);
    _psx_rt_sigprocmask(SIG_BLOCK, &mask, &orig);
    _psx_rt_sigaction(psx_tracker.psx_sig, NULL, &existing_sa);
    if (existing_sa.sa_handler != (void *) &psx_posix_syscall_actor) {
	memcpy(&actions->chained_action, &existing_sa,
	       sizeof(struct sigaction));
	/*
	 * no less masking etc than original handler (some
	 * architectures hook things on the restorer etc)
	 */
	memcpy(&actions->sig_action, &existing_sa, sizeof(existing_sa));
	actions->sig_action.sa_handler = (void *) &psx_posix_syscall_actor;
	actions->sig_action.sa_flags |= SA_SIGINFO | SA_ONSTACK | SA_RESTART
#ifdef SA_RESTORER
	    | SA_RESTORER;
	/* if we need a restorer, and nothing is set, use our own */
	if (actions->sig_action.sa_restorer == 0) {
	    actions->sig_action.sa_restorer = (void *) &psx_restorer;
	}
#else
	;
#endif /* def SA_RESTORER */
	_psx_rt_sigaction(psx_tracker.psx_sig, &actions->sig_action, NULL);
    }
    _psx_rt_sigprocmask(SIG_SETMASK, &orig, NULL);
}
