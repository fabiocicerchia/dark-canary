@domain:safety-and-access @area:access-token @from:cmd/dark-canary
Feature: Guarding captured production data with an access token
  Operators need the captured production bodies that dark-canary holds to be unreachable by anyone without the shared secret, and the tool must refuse to run exposed and open at the same time.

  Background:
    Given the collector holds captures that quote production response bodies

  @id:safety-and-access.access-token.001 @rule:access.token.loopback-needs-no-token @verified @source:cmd/dark-canary/config.go#parseFlags @test:cmd/dark-canary/main_test.go#TestNoTokenIsFineOnLoopback
  Scenario: A collector on the default loopback address needs no token
    Given the collector listens on the default address 127.0.0.1:8099
    And no token was configured
    When a client calls GET /stats without a token
    Then the response status is 200
