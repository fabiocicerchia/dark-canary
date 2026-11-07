@domain:noise-suppression @area:default-rules @from:noise/noise
Feature: Starting from a built-in noise ruleset
  Gives operators on a fresh install a short ruleset that hides only the header differences every pair of servers has, so findings are readable from day one without hiding real ones.

  Background:
    Given the operator runs dark-canary without a rules file

  @id:noise-suppression.default-rules.001 @rule:noise.defaults.ignore-per-request-headers @verified @source:internal/noise/noise.go#Default @test:internal/noise/noise_test.go#TestUnmatchedPathsAreNeverSuppressed @test:internal/noise/noise_test.go#TestRulesetSuppressesThroughTheEngine
  Scenario Outline: Headers that differ on every request are not reported by default
    Given a primary response that sends the <header> header and an otherwise identical shadow response that does not
    When the pair is compared
    Then no difference is reported for /headers/<header>
    And the pair counts 1 suppressed difference

    Examples:
      | header           |
      | date             |
      | server           |
      | x-request-id     |
      | x-dark-canary-id |
      | set-cookie       |
      | age              |
      | etag             |

  @id:noise-suppression.default-rules.002 @rule:noise.defaults.never-hide-status-or-body @verified @source:internal/noise/noise.go#Default @test:internal/noise/noise_test.go#TestUnmatchedPathsAreNeverSuppressed
  Scenario Outline: The default ruleset never hides a status or body difference
    Given a primary and a shadow response whose <location> differs
    When the pair is compared
    Then the difference at <location> is reported
    And the pair counts 0 suppressed differences

    Examples:
      | location    |
      | /status     |
      | /body/total |
