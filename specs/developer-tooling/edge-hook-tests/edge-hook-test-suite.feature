@domain:developer-tooling @area:edge-hook-tests @from:lua/dark-canary-test
Feature: Running the edge capture hook's test suite
  Lets a contributor check the nginx capture hook on any plain Lua 5.1+ interpreter, with no OpenResty or luarocks, so a hook change that would stop production traffic from being mirrored or paired fails before it merges.

  Background:
    Given the contributor is at the repository root with a Lua 5.1 or newer interpreter installed

  @id:developer-tooling.edge-hook-tests.001 @rule:devtooling.edge-hook-tests.exit-status-reflects-failures @inferred @source:lua/dark_canary_test.lua#test
  Scenario Outline: The edge hook suite reports every test and fails the run when any test fails
    Given the capture hook has <state>
    When the contributor runs the edge hook test suite
    Then each test is listed with "ok" or "FAIL" before its name
    And a closing line gives the number of tests run and "<failed> failed", followed by the reason for each failure
    And the run exits with status <exit>

    Examples:
      | state                | failed | exit |
      | no regressions       | 0      | 0    |
      | one broken behaviour | 1      | 1    |

  @id:developer-tooling.edge-hook-tests.002 @rule:devtooling.edge-hook-tests.payload-drift-fails @verified @source:lua/dark_canary_test.lua#fixture @test:lua/dark_canary_test.lua#the primary payload matches the fixture the collector decodes @test:lua/dark_canary_test.lua#the shadow payload matches the fixture the collector decodes
  Scenario: A change to the payload the edge ships to the collector fails the suite until the pinned fixture is updated
    Given the pinned primary and shadow capture fixtures are in the repository
    And a hook change alters the JSON the edge would post to the collector
    When the contributor runs the edge hook test suite
    Then the primary or shadow payload test is marked FAIL, naming the lua/testdata fixture it no longer matches
    And the run exits with status 1

  @id:developer-tooling.edge-hook-tests.003 @rule:devtooling.edge-hook-tests.fixture-refresh-on-request @inferred @source:lua/dark_canary_test.lua#fixture
  Scenario: The contributor can re-pin the collector payload fixtures after a deliberate change
    Given a deliberate hook change alters the payload the edge ships
    When the contributor runs the edge hook test suite with DC_UPDATE_FIXTURES set to 1
    Then lua/testdata/edge_primary.json and lua/testdata/edge_shadow.json are rewritten with the new payloads
    And the payload tests are marked ok
