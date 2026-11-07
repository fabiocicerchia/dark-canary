@domain:noise-suppression @area:rule-files @from:noise/noise
Feature: Tuning noise with an operator rules file
  Lets operators tune suppression with a YAML file merged over the defaults, and refuses a file that would quietly do nothing or be misread, before it can skew a report.

  Background:
    Given the operator starts dark-canary with a rules file

  @id:noise-suppression.rule-files.001 @rule:noise.rule-files.reject-unusable-rules @verified @source:internal/noise/noise.go#Load @test:internal/noise/noise_test.go#TestLoadRejectsRulesThatDoNothing
  Scenario Outline: A rules file containing an unusable rule is refused
    Given the rules file contains <content>
    When the operator starts dark-canary
    Then dark-canary refuses the ruleset with an error naming the file and <problem>
    And no comparison runs with that ruleset

    Examples:
      | content                                                 | problem                                          |
      | a rule with ignore: true and no path                    | rule 0 has no path                               |
      | a rule for /body/ts that neither ignores nor normalises | rule 0 (/body/ts) neither ignores nor normalises |
      | a rule for /x with normalise: round:banana              | round: needs 0..15 decimal places                |
      | a rule for /x with normalise: round:16                  | round: needs 0..15 decimal places                |
      | a rule for /x with normalise: vibes                     | unknown normalisation "vibes"                    |
      | text that is not valid YAML                             | the YAML parse error                             |

  @id:noise-suppression.rule-files.002 @rule:noise.rule-files.missing-file-refused @inferred @source:internal/noise/noise.go#Load
  Scenario: A rules file that cannot be read is refused rather than ignored
    Given the rules file path points at a file that does not exist
    When the operator starts dark-canary
    Then dark-canary refuses the ruleset with the file's read error
    And it does not fall back to the default ruleset alone
