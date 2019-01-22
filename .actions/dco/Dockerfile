FROM alpine:latest
LABEL "com.github.actions.name"="Validate DCO"
LABEL "com.github.actions.description"="Validate commits include DCO"
LABEL "com.github.actions.icon"="code"
LABEL "com.github.actions.color"="blue"

LABEL "repository"="http://github.com/moby/buildkit"
LABEL "homepage"="http://github.com/moby/buildkit"
LABEL "maintainer"="Evan Hazlett <ejhazlett@gmail.com>"

RUN apk add --no-cache -U git bash
ADD entrypoint.sh /entrypoint.sh
ENTRYPOINT ["/entrypoint.sh"]
