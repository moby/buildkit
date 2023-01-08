package watchdog

import "log"

// logger is an interface to be implemented by custom loggers.
type logger interface {
	Debugf(template string, args ...interface{})
	Infof(template string, args ...interface{})
	Warnf(template string, args ...interface{})
	Errorf(template string, args ...interface{})
}

var _ logger = (*stdlog)(nil)

// stdlog is a logger that proxies to a standard log.logger.
type stdlog struct {
	log   *log.Logger
	debug bool
}

func (s *stdlog) Debugf(template string, args ...interface{}) {
	if !s.debug {
		return
	}
	s.log.Printf(template, args...)
}

func (s *stdlog) Infof(template string, args ...interface{}) {
	s.log.Printf(template, args...)
}

func (s *stdlog) Warnf(template string, args ...interface{}) {
	s.log.Printf(template, args...)
}

func (s *stdlog) Errorf(template string, args ...interface{}) {
	s.log.Printf(template, args...)
}
