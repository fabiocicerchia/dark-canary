@domain:configuration-and-lifecycle @area:startup-configuration @from:safety/safety
Feature: Starting with a safe posture
  Serves the operator by starting dark-canary safe by default and refusing, or loudly flagging, settings that would weaken a guard rail.

  @id:configuration-and-lifecycle.startup-configuration.001 @rule:safety.config.safe-defaults @verified @source:internal/safety/safety.go#Default @test:internal/safety/safety_test.go#TestDefaultsAreTheSafePosture
  Scenario: A fresh start mirrors only reads, samples 1% and has a kill file
    Given the operator sets no safety options
    When the operator starts dark-canary
    Then only GET and HEAD requests are mirrored
    And the sample rate is 0.01
    And the kill file is /etc/dark-canary/kill
    And the largest accepted capture body is 1048576 bytes
