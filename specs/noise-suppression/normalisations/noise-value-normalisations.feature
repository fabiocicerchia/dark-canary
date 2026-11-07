@domain:noise-suppression @area:normalisations @from:noise/noise
Feature: Treating near-equal values as agreeing
  Lets operators ignore a location outright or require only approximate agreement there (rounding, ordering, whitespace, case, length), while keeping every suppression counted so it is never mistaken for agreement.

  @id:noise-suppression.normalisations.001 @rule:noise.normalise.agreeing-values-not-counted @verified @source:internal/noise/noise.go#compare @test:internal/noise/noise_test.go#TestEqualValuesAreNeverCountedAsSuppressed
  Scenario Outline: Values that already agree are not counted as suppressed differences
    Given a rule normalises /x with <normalise>
    And the primary and the shadow both have <value> at /x
    When the pair is compared
    Then no difference is reported at /x
    And the pair counts 0 suppressed differences

    Examples:
      | normalise | value     |
      | sort      | ["a","b"] |
      | round:2   | 1         |
