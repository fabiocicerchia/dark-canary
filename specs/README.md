# Specifications of dark-canary

What this code does, one atomic Gherkin scenario per behaviour, read from the code by code-atlas.
Tags: `@verified` a test asserts it · `@inferred` read from the code only · `@gap` an entry point no test covers · `@human` written or amended by a person, never rewritten.

## Configuration and Lifecycle
How the service is configured, started, health-checked and stopped.

- **Exit codes**: [Telling failures apart by exit code](configuration-and-lifecycle/exit-codes/exit-codes.feature) (2)
- **Health check and shutdown**: [Health checks and shutdown](configuration-and-lifecycle/health-and-shutdown/health-and-shutdown.feature) (2)
- **Startup configuration**: [Starting with a safe posture](configuration-and-lifecycle/startup-configuration/safety-posture-at-startup.feature) (1)

## Developer Tooling and Packaging
Building, testing, linting and shipping dark-canary.

- **Container image**: [Running dark-canary from the container image](developer-tooling/container-image/container-image.feature) (4)
- **Edge hook test suite**: [Running the edge capture hook's test suite](developer-tooling/edge-hook-tests/edge-hook-test-suite.feature) (3)

## Noise Suppression
Declaratively ignoring the differences that are expected, so a reported diff means something.

- **Default ruleset**: [Starting from a built-in noise ruleset](noise-suppression/default-rules/default-noise-rules.feature) (2)
- **Value normalisations**: [Treating near-equal values as agreeing](noise-suppression/normalisations/noise-value-normalisations.feature) (1)
- **Path matching**: [Targeting noise rules at locations in a response](noise-suppression/path-matching/noise-rule-path-matching.feature) (1)
- **Operator rule files**: [Tuning noise with an operator rules file](noise-suppression/rule-files/operator-noise-rule-files.feature) (2)

## Reporting
Telling the operator whether the shadow behaves the same as the primary, and why not if it doesn't.

- **Agreement summary**: [Quoting how often the shadow agrees with production](reporting/agreement-summary/agreement-summary.feature) (1)
- **Severity grouping and ranking**: [Ranking differences by how much they should worry the operator](reporting/severity-ranking/severity-ranking.feature) (2)

## Response Comparison
Deciding structurally whether the shadow answered the same way the primary did.

- **Value equality**: [Deciding when two values are equal](response-comparison/value-equality/value-equality.feature) (3)

## Safety and Access Control
Guard rails that keep the shadow away from users and data, and keep the tool from being exposed.

- **Access token and exposure**: [Guarding captured production data with an access token](safety-and-access/access-token/access-token.feature) (1)
- **Sensitive data scrubbing**: [Scrubbing request bodies at the edge](safety-and-access/data-scrubbing/edge-request-body-scrubbing.feature) (3), [Scrubbing sensitive data from captures](safety-and-access/data-scrubbing/sensitive-data-scrubbing.feature) (2)
- **Shadow isolation**: [Keeping the shadow away from users](safety-and-access/shadow-isolation/shadow-isolation.feature) (1)

## Traffic Mirroring
Getting a copy of production traffic to the shadow deployment while the user is served by the primary.

- **Capture shipping**: [Shipping edge captures to the collector](traffic-mirroring/capture-shipping/edge-capture-shipping.feature) (5)
- **Edge capture hook**: [Capturing mirrored traffic at the edge](traffic-mirroring/edge-capture-hook/edge-capture.feature) (1)
