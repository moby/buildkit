#ifndef LIBPSX_H
#define LIBPSX_H

/*
 * Copyright (c) 2025 Andrew G. Morgan <morgan@kernel.org>
 *
 * These header definitions are private to the PSX mechanism. External
 * API references are to be found in <sys/psx_syscall.h>
 *
 * Since we no longer (libcap-2.72) operate at the pthreads
 * abstraction, we need our own mutex etc implementation.
 */

typedef unsigned char psx_mutex_t;
#define _psx_mu_blocked(x)					\
    __atomic_test_and_set((void *)(x), __ATOMIC_SEQ_CST)
#define _psx_mu_lock(x)             \
    while (_psx_mu_blocked(x)) sched_yield()
#define _psx_mu_unlock(x)           \
    __atomic_clear((void *)(x), __ATOMIC_SEQ_CST)
#define _psx_mu_unlock_return(x, y) \
    do { _psx_mu_unlock(x); return (y); } while (0)
#define _psx_mu_cond_wait(x)       \
    do {                           \
        _psx_mu_unlock(x);         \
        sched_yield();             \
        _psx_mu_lock(x);           \
    } while (0)

/* Not reliably defined by *libc so, alias the direct syscall. */
#define _psx_gettid() syscall(SYS_gettid)

extern void psx_lock(void);
extern void psx_unlock(void);
extern void psx_cond_wait(void);
extern long psx_mix(long value);

typedef enum {
    _PSX_IDLE = 0,
    _PSX_SETUP = 1,
    _PSX_SYSCALL = 2,
    _PSX_EXITING = 3,
} psx_tracker_state_t;

/*
 * Tracking threads is done via a hash map of these objects.
 */
typedef struct psx_thread_ref_s {
    long sweep;
    long pending;
    long tid;
    long retval;
} psx_thread_ref_t;

/*
 * This global structure holds the global coordination state for
 * libcap's psx_syscall() support.
 */
typedef struct {
    long pid;
    char *pid_path;

    psx_mutex_t state_mu;
    psx_tracker_state_t state;
    int initialized;
    int incomplete;
    int psx_sig;
    int force_failure; /* leave this as zero to avoid forcing a crash */
    psx_sensitivity_t sensitivity;

    struct {
	long syscall_nr;
	long arg1, arg2, arg3, arg4, arg5, arg6;
	int six;
	int active;
    } cmd;

    /* This is kept opaque here, but its details are known to psx_calls.c */
    void *actions;

    int map_entries;
    long map_mask;
    psx_thread_ref_t *map;
} psx_tracker_t;

/* defined in psx_calls.c */
extern psx_tracker_t psx_tracker;
extern int psx_actions_size(void);
extern void psx_confirm_sigaction(void);

#endif /* def LIBPSX_H */
