FROM busybox
ENV --unset FOO BAR
ENV --unset=true FOO
ENV --unset=false FOO=bar
LABEL --unset foo bar.baz
LABEL --unset=false foo=bar
VOLUME --unset /foo /bar
VOLUME --unset ["/foo", "/bar"]
ONBUILD ENV --unset FOO
