@domain:developer-tooling @area:container-image @from:dark-canary/dockerfile
Feature: Running dark-canary from the container image
  Operators who deploy dark-canary as a container rely on the image to start with a known listener layout, to refuse unauthenticated exposure, and to run without root privileges.

  Background:
    Given the operator runs the published dark-canary container image

  @id:developer-tooling.container-image.001 @rule:packaging.container-image.default-command-requires-token @inferred @source:Dockerfile#CMD @questionable
  Scenario: The image's default command refuses to start without an access token
    Given the image's default command binds the collector to 0.0.0.0:8099 and the proxy to 0.0.0.0:8080
    And no access token is configured
    When the operator starts the container with no arguments
    Then dark-canary refuses to start
    And it reports "-listen 0.0.0.0:8099 is not loopback: set -token, or bind to 127.0.0.1"
    And the process exits with code 64

  @id:developer-tooling.container-image.002 @rule:packaging.container-image.arguments-replace-default-listeners @inferred @source:Dockerfile#CMD @questionable
  Scenario: Passing only a token makes the container listen on loopback, unreachable from outside
    Given the image's default command is the only place the listeners are set to every interface
    When the operator starts the container with only "-token s3cret" as arguments
    Then dark-canary starts
    And the collector listens on 127.0.0.1:8099 inside the container and no proxy listener is started
    And neither published port 8099 nor 8080 answers from outside the container

  @id:developer-tooling.container-image.003 @rule:packaging.container-image.runs-unprivileged-without-shell @inferred @source:Dockerfile#USER
  Scenario: The container runs as an unprivileged user with no shell inside
    Given the container is running
    When the operator inspects the running container
    Then the dark-canary process runs as user 65532 and group 65532, not root
    And the image holds only the dark-canary binary and the CA certificate bundle
    And opening a shell in the container fails because the image has none

  @id:developer-tooling.container-image.004 @rule:packaging.container-image.verifies-https-upstreams @inferred @source:Dockerfile#COPY
  Scenario: The container can mirror to primary and shadow upstreams served over HTTPS
    Given the primary and shadow upstreams use https URLs with certificates from a public certificate authority
    When the operator starts the container in proxy mode with -proxy-listen 0.0.0.0:8080, -primary https://primary.example.com and -shadow https://shadow.example.com
    Then clients get the primary's responses through port 8080
    And the shadow receives the mirrored requests with no certificate verification failures
