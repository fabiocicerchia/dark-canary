@domain:response-comparison @area:value-equality @from:diff/diff
Feature: Deciding when two values are equal
  Makes sure serialisation choices (key order, whitespace, how a number is written) are not reported as divergence, so an identical shadow reads as identical.

  Background:
    Given Given a primary and a shadow response captured for the same request

  @id:response-comparison.value-equality.001 @rule:response.value-equality.formatting-and-key-order-ignored @verified @source:internal/diff/diff.go#decodeJSON @test:internal/diff/diff_test.go#TestKeyOrderIsNotADifference @test:internal/diff/diff_test.go#TestWhitespaceIsNotADifference @test:internal/diff/diff_test.go#FuzzDiffBodies
  Scenario Outline: JSON bodies that differ only in key order or whitespace are identical
    Given the primary body is <primary>
    And the shadow body is <shadow>
    When dark-canary compares the pair
    Then the pair is reported identical

    Examples:
      | primary       | shadow                                      |
      | {"a":1,"b":2} | {"b":2,"a":1}                               |
      | {"a":[1,2]}   | { "a": [ 1, 2 ] } spread over several lines |

  @id:response-comparison.value-equality.002 @rule:response.value-equality.numbers-compare-numerically @verified @source:internal/diff/diff.go#scalarEqual @test:internal/diff/diff_test.go#TestNumbersCompareNumerically
  Scenario: Numbers written differently but equal in value are identical
    Given the primary body is {"a":1.0}
    And the shadow body is {"a":1}
    When dark-canary compares the pair
    Then the pair is reported identical

  @id:response-comparison.value-equality.003 @rule:response.value-equality.large-integer-precision @inferred @source:internal/diff/diff.go#scalarEqual @questionable
  Scenario Outline: Different integers too large for float precision are reported identical
    Given the primary body is {"id":<primary_id>}
    And the shadow body is {"id":<shadow_id>}
    When dark-canary compares the pair
    Then the pair is reported identical

    Examples:
      | primary_id        | shadow_id         |
      | 9007199254740993  | 9007199254740992  |
      | 10000000000000001 | 10000000000000000 |
