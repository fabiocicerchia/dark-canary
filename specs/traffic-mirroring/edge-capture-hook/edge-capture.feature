@domain:traffic-mirroring @area:edge-capture-hook @from:lua/dark-canary
Feature: Capturing mirrored traffic at the edge
  Lets operators running nginx record both sides of the mirror at the edge, labelled and shaped so the collector can pair and compare them.

  Background:
    Given the capture hook is installed on both the primary and the shadow server

  @id:traffic-mirroring.edge-capture-hook.001 @rule:edge.config.partial-collector-override-keeps-defaults @verified @source:lua/dark_canary.lua#merge @test:lua/dark_canary_test.lua#a partial collector override keeps the other collector fields @test:lua/dark_canary_test.lua#the capture is stamped with its side and shipped to the collector
  Scenario: Overriding one collector setting keeps the defaults for the rest
    Given the hook is configured with only the collector host "10.0.0.5"
    When the edge node ships a capture
    Then the capture is posted to 10.0.0.5 on port 8099 at /captures
    And the collector timeout stays at 1000 ms
