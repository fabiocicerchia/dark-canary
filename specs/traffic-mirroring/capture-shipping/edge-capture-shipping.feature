@domain:traffic-mirroring @area:capture-shipping @from:lua/dark-canary
Feature: Shipping edge captures to the collector
  Lets operators get edge captures to an authenticated, optionally TLS-fronted collector, without leaking the token and with every failure visible in the nginx log.

  Background:
    Given the edge node has captured a request

  @id:traffic-mirroring.capture-shipping.001 @rule:edge.shipping.collector-token-header @verified @source:lua/dark_canary.lua#ship @test:lua/dark_canary_test.lua#the collector token is sent when configured @test:lua/dark_canary_test.lua#no token configured sends no token header
  Scenario Outline: The configured collector token is sent with every capture
    Given the collector token is configured as <token>
    When the edge node ships the capture
    Then the capture is posted to the collector <token_header>

    Examples:
      | token    | token_header                                |
      | "s3cret" | with the header X-Dark-Canary-Token: s3cret |
      | nothing  | without an X-Dark-Canary-Token header       |

  @id:traffic-mirroring.capture-shipping.002 @rule:edge.shipping.token-with-line-break-refused @verified @source:lua/dark_canary.lua#ship @test:lua/dark_canary_test.lua#a token with a line break is refused rather than sent
  Scenario: A collector token containing a line break is never sent
    Given the collector token is configured as "s3cret\r\nX-Evil: 1"
    When the edge node ships the capture
    Then nothing is posted to the collector
    And the nginx error log shows the error "dark-canary: collector.token contains a line break; not shipping"

  @id:traffic-mirroring.capture-shipping.003 @rule:edge.shipping.tls-certificate-verified @verified @source:lua/dark_canary.lua#post @test:lua/dark_canary_test.lua#tls verifies the collector's certificate for its name @test:lua/dark_canary_test.lua#a failed tls handshake sends nothing, token included
  Scenario Outline: Over TLS the collector's certificate is always verified for its name
    Given TLS to the collector is turned on with collector host "10.0.0.5" and server name <server_name>
    When the edge node ships the capture
    Then the collector's certificate is verified for <verified_name>
    And the capture is posted only after the handshake succeeds

    Examples:
      | server_name          | verified_name      |
      | "collector.internal" | collector.internal |
      | not set              | 10.0.0.5           |

  @id:traffic-mirroring.capture-shipping.004 @rule:edge.shipping.failed-tls-handshake-sends-nothing @verified @source:lua/dark_canary.lua#post @test:lua/dark_canary_test.lua#a failed tls handshake sends nothing, token included
  Scenario: A collector that fails certificate verification receives nothing, not even the token
    Given TLS to the collector is turned on and the collector token is "s3cret"
    And the collector presents a certificate that fails verification
    When the edge node ships the capture
    Then nothing is posted to the collector, the token included
    And the nginx error log shows the warning "dark-canary: collector TLS handshake failed: certificate verify failed"

  @id:traffic-mirroring.capture-shipping.005 @rule:edge.shipping.collector-failures-logged @verified @source:lua/dark_canary.lua#post @test:lua/dark_canary_test.lua#an unreachable collector is logged, not raised @test:lua/dark_canary_test.lua#a capture the collector refuses is logged @test:lua/dark_canary_test.lua#an accepted capture logs nothing
  Scenario Outline: A collector that is unreachable or refuses a capture shows up in the nginx log
    Given the collector <collector_state>
    When the edge node ships the capture
    Then the edge node raises no error
    And the nginx error log shows <log>

    Examples:
      | collector_state                   | log                                                                             |
      | is unreachable                    | the warning "dark-canary: collector unreachable: connection refused"            |
      | answers HTTP/1.1 401 Unauthorized | the warning "dark-canary: collector refused capture: HTTP/1.1 401 Unauthorized" |
      | answers HTTP/1.1 202 Accepted     | nothing                                                                         |
