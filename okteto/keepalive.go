package okteto

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/keepalive"
)

var (
	kcp *keepalive.ClientParameters
	ksp *keepalive.ServerParameters
	kep *keepalive.EnforcementPolicy
)

func LoadKeepaliveClientParams() keepalive.ClientParameters {
	if kcp == nil {
		kcp = &keepalive.ClientParameters{}
		if timeEnv := os.Getenv("OKTETO_KEEPALIVE_CLIENT_TIME_MS"); timeEnv != "" {
			timeDuration, err := time.ParseDuration(fmt.Sprintf("%sms", timeEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_CLIENT_TIME_MS value: %s", err.Error()))
			} else {
				kcp.Time = timeDuration
			}
		}

		if timeoutEnv := os.Getenv("OKTETO_KEEPALIVE_CLIENT_TIMEOUT_MS"); timeoutEnv != "" {
			timeoutDuration, err := time.ParseDuration(fmt.Sprintf("%sms", timeoutEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_CLIENT_TIMEOUT_MS value: %s", err.Error()))
			} else {
				kcp.Timeout = timeoutDuration
			}
		}

		if permitWithoutStreamEnv := os.Getenv("OKTETO_KEEPALIVE_CLIENT_PERMIT_WITHOUT_STREAM"); permitWithoutStreamEnv != "" {
			permitWithoutStream, err := strconv.ParseBool(permitWithoutStreamEnv)
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_CLIENT_PERMIT_WITHOUT_STREAM value: %s", err.Error()))
			} else {
				kcp.PermitWithoutStream = permitWithoutStream
			}
		}
	}

	return *kcp
}

func LoadKeepaliveServerParams() keepalive.ServerParameters {
	if ksp == nil {
		ksp = &keepalive.ServerParameters{}
		if maxConnEnv := os.Getenv("OKTETO_KEEPALIVE_SERVER_MAX_CONN_IDLE_MS"); maxConnEnv != "" {
			maxConnIdleDuration, err := time.ParseDuration(fmt.Sprintf("%sms", maxConnEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_SERVER_MAX_CONN_IDLE_MS value: %s", err.Error()))
			} else {
				ksp.MaxConnectionIdle = maxConnIdleDuration
			}
		}

		if maxConnAgeEnv := os.Getenv("OKTETO_KEEPALIVE_SERVER_MAX_CONN_AGE_MS"); maxConnAgeEnv != "" {
			maxConnAgeDuration, err := time.ParseDuration(fmt.Sprintf("%sms", maxConnAgeEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_SERVER_MAX_CONN_AGE_MS value: %s", err.Error()))
			} else {
				ksp.MaxConnectionAge = maxConnAgeDuration
			}
		}

		if maxConnAgeGraceEnv := os.Getenv("OKTETO_KEEPALIVE_SERVER_MAX_CONN_AGE_GRACE_MS"); maxConnAgeGraceEnv != "" {
			maxConnAgeGraceDuration, err := time.ParseDuration(fmt.Sprintf("%sms", maxConnAgeGraceEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_SERVER_MAX_CONN_AGE_GRACE_MS value: %s", err.Error()))
			} else {
				ksp.MaxConnectionAgeGrace = maxConnAgeGraceDuration
			}
		}

		if timeEnv := os.Getenv("OKTETO_KEEPALIVE_SERVER_TIME_MS"); timeEnv != "" {
			timeDuration, err := time.ParseDuration(fmt.Sprintf("%sms", timeEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_SERVER_TIME_MS value: %s", err.Error()))
			} else {
				ksp.Time = timeDuration
			}
		}

		if timeoutEnv := os.Getenv("OKTETO_KEEPALIVE_SERVER_TIMEOUT_MS"); timeoutEnv != "" {
			timeoutDuration, err := time.ParseDuration(fmt.Sprintf("%sms", timeoutEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_SERVER_TIMEOUT_MS value: %s", err.Error()))
			} else {
				ksp.Timeout = timeoutDuration
			}
		}
	}

	return *ksp
}

func LoadKeepaliveEnforcementPolicy() keepalive.EnforcementPolicy {
	if kep == nil {
		kep = &keepalive.EnforcementPolicy{}
		if mintimeEnv := os.Getenv("OKTETO_KEEPALIVE_SERVER_POLICY_MINTIME_MS"); mintimeEnv != "" {
			mintimeDuration, err := time.ParseDuration(fmt.Sprintf("%sms", mintimeEnv))
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_SERVER_POLICY_MINTIME_MS value: %s", err.Error()))
			} else {
				kep.MinTime = mintimeDuration
			}
		}

		if permitWithoutStreamEnv := os.Getenv("OKTETO_KEEPALIVE_SERVER_POLICY_PERMIT_WITHOUT_STREAM"); permitWithoutStreamEnv != "" {
			permitWithoutStream, err := strconv.ParseBool(permitWithoutStreamEnv)
			if err != nil {
				logrus.Info(fmt.Sprintf("couldn't parse OKTETO_KEEPALIVE_SERVER_POLICY_PERMIT_WITHOUT_STREAM value: %s", err.Error()))
			} else {
				kep.PermitWithoutStream = permitWithoutStream
			}
		}
	}

	return *kep
}
