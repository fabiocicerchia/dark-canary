@domain:safety-and-access @area:data-scrubbing @from:lua/dark-canary
Feature: Scrubbing request bodies at the edge
  Keeps passwords, tokens and other configured secrets in request bodies from ever reaching the collector in clear.

  Background:
    Given the edge node captures a request that carries a body

  @id:safety-and-access.data-scrubbing.001 @rule:edge.scrubbing.secret-keys-redacted-in-request-body @verified @source:lua/dark_canary.lua#request_body @test:lua/dark_canary_test.lua#scrub_args keys are redacted in a form-encoded request body @test:lua/dark_canary_test.lua#a percent-encoded form key is redacted as the key it decodes to @test:lua/dark_canary_test.lua#the kit's default scrub_args apply when none are configured @test:lua/dark_canary_test.lua#the request body is captured, scrubbed and base64-encoded @test:lua/dark_canary_test.lua#scrub_args keys reach the JSON scrubber too
  Scenario Outline: Secret fields in a request body are redacted before it leaves the edge
    Given the configured secret keys are <keys>
    And the request body is <body>
    When the edge node ships the capture
    Then the shipped request body is <shipped>

    Examples:
      | keys                                               | body                                               | shipped                                                                 |
      | scrub_args password, token                         | user=ann&Password=hunter2&next=%2F&token=          | user=ann&Password=[redacted]&next=%2F&token=[redacted]                  |
      | scrub_args password, token                         | user=ann&pass%77ord=hunter2&to%6Ben=t&PASS%57ORD=x | user=ann&pass%77ord=[redacted]&to%6Ben=[redacted]&PASS%57ORD=[redacted] |
      | none configured (kit default scrub_args: password) | password=hunter2                                   | password=[redacted]                                                     |
      | scrub_fields password                              | {"password":"hunter2","q":1}                       | {"password":"[redacted]","q":1}                                         |

  @id:safety-and-access.data-scrubbing.002 @rule:edge.scrubbing.multipart-body-withheld @verified @source:lua/dark_canary.lua#request_body @test:lua/dark_canary_test.lua#a multipart request body is not shipped
  Scenario: A multipart request body is never shipped
    Given the request is multipart/form-data with a field password=hunter2
    When the edge node ships the capture
    Then the capture is shipped without a request body

  @id:safety-and-access.data-scrubbing.003 @rule:edge.scrubbing.body-scrubbed-then-capped @verified @source:lua/dark_canary.lua#request_body @test:lua/dark_canary_test.lua#a request body over max_body is cut at it @test:lua/dark_canary_test.lua#the request body is scrubbed before it is cut @test:lua/dark_canary_test.lua#max_body = 0 captures no request body on either side
  Scenario Outline: A request body is redacted first and then cut to the configured size
    Given max_body is <max_body>
    And the request body is <body>
    When the edge node ships the capture
    Then the shipped request body is <shipped>

    Examples:
      | max_body | body                                              | shipped                                     |
      | 4        | xxxxxxxxxx                                        | xxxx                                        |
      | 16       | {"password":"hunter2"} with password a secret key | {"password":"[re                            |
      | 0        | {"id":7}                                          | absent, on the primary and the shadow alike |
