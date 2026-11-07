@domain:safety-and-access @area:shadow-isolation @from:cmd/dark-canary
Feature: Keeping the shadow away from users
  Users must never notice the shadow: a shadow that is broken, slow or redirecting cannot change or delay what the client gets.

  Background:
    Given dark-canary runs in proxy mode with the sample rate at 1
    And the primary answers GET /health with 200 {"ok":true}

  @id:safety-and-access.shadow-isolation.001 @rule:isolation.shadow.redirects-not-followed @inferred @source:cmd/dark-canary/proxy.go#newProxy
  Scenario: A redirect from the shadow is never followed
    Given the shadow answers GET /health with a 302 to http://other.example.com/health
    When a client sends GET /health through the proxy
    Then no request is sent to other.example.com
    And the shadow's 302 is what gets compared against the primary's 200
