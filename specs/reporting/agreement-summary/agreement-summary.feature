@domain:reporting @area:agreement-summary @from:report/report
Feature: Quoting how often the shadow agrees with production
  Gives the operator the headline numbers — pairs compared, identical, divergent, suppressed — and the agreement rate they quote when deciding go or no-go on the shadow.

  Background:
    Given dark-canary is comparing primary and shadow responses

  @id:reporting.agreement-summary.001 @rule:reporting.summary.empty-is-not-agreement @verified @source:internal/report/report.go#Text @test:internal/report/report_test.go#TestNoPairsSaysSoRatherThanClaimingAgreement
  Scenario Outline: A report with nothing compared says so instead of claiming agreement
    Given <state>
    When the operator reads the text report
    Then the report says "<message>"

    Examples:
      | state                                 | message                                                                       |
      | no pairs have been compared yet       | No pairs yet. Check that both paths are reporting: /stats shows what arrived. |
      | pairs were compared and none diverged | No divergence.                                                                |
