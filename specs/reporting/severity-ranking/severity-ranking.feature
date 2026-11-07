@domain:reporting @area:severity-ranking @from:report/report
Feature: Ranking differences by how much they should worry the operator
  Turns thousands of individual differences into a short list of findings, worst first, so the operator acts on what matters rather than on what is most frequent.

  Background:
    Given dark-canary is comparing primary and shadow responses

  @id:reporting.severity-ranking.001 @rule:reporting.groups.severity-by-kind @verified @source:internal/report/report.go#severityOf @test:internal/report/report_test.go#TestSeverityOutranksFrequency @questionable
  Scenario Outline: Each kind of difference carries a fixed severity
    Given a pair differs only by a <kind> difference
    When the operator reads the report
    Then the finding is listed with severity <severity>

    Examples:
      | kind       | severity |
      | status     | high     |
      | shape      | medium   |
      | body_key   | medium   |
      | header     | low      |
      | body_value | low      |

  @id:reporting.severity-ranking.002 @rule:reporting.text.one-line-per-finding @verified @source:internal/report/report.go#Text @test:internal/report/report_test.go#TestTextRendersOneLinePerGroup @questionable
  Scenario: Each finding takes one line of the text report with a shortened example
    Given a finding's primary value spans two lines and its shadow value is 80 bytes long
    When the operator reads the text report
    Then the finding appears as one row with severity, count, rate, kind, path and primary → shadow
    And newlines and tabs in the example values are shown as spaces
    And example values longer than 40 bytes are cut to 40 bytes and end with "…"
