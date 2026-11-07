@domain:configuration-and-lifecycle @area:exit-codes @from:cmd/dark-canary
Feature: Telling failures apart by exit code
  Supervisors and operators need distinct exit codes so a flag mistake, a bad ruleset and a taken port can be told apart without parsing stderr.

  @id:configuration-and-lifecycle.exit-codes.001 @rule:lifecycle.exit.codes-classify-failures @verified @source:cmd/dark-canary/exit.go#exitCode @test:cmd/dark-canary/exit_test.go#TestExitCodeClassifiesEveryFailure @test:cmd/dark-canary/exit_test.go#TestExitCodesAreDistinctAndInRange
  Scenario Outline: Each kind of failure has its own sysexits-style exit code
    Given <failure>
    When the operator starts dark-canary
    Then the process exits with code <code>

    Examples:
      | failure                                              | code |
      | a flag value or flag combination dark-canary refuses | 64   |
      | the -rules file was read but does not hold up        | 65   |
      | the -rules file does not exist                       | 66   |
      | the collector or proxy address is already in use     | 71   |
      | a failure with no better classification              | 70   |

  @id:configuration-and-lifecycle.exit-codes.002 @rule:lifecycle.exit.bind-failure-prints-no-report @gap @source:cmd/dark-canary/main.go#run
  Scenario: A listener that never came up prints no empty report
    Given 127.0.0.1:8099 is already taken by another process
    When the operator starts dark-canary
    Then stderr shows "dark-canary failed" with the bind error
    And no report is printed
    And the process exits with code 71
