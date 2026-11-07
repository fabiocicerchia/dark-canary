@domain:documentation-site @area:site-theme @from:stylesheets/extra
Feature: Branding the documentation site
  Gives readers of the dark-canary docs and splash page one consistent amber brand that renders offline and legibly in both light and dark schemes.

  Background:
    Given the documentation site and the splash page are published together

  @id:docs.documentation-site.site-theme.001 @rule:docs.theme.single-shared-palette @inferred @source:docs/stylesheets/extra.css#:root
  Scenario: The splash page and the docs show the same amber accent colours
    Given the docs theme defines the accent pair amber #fbbf24 and dark amber #d97706
    When a reader opens the splash page and then the rendered docs
    Then the splash title and its primary button use a gradient from #fbbf24 to #d97706
    And the docs header, primary buttons and hero title use the same two colours
    And changing the accent pair in the docs theme changes both pages

  @id:docs.documentation-site.site-theme.002 @rule:docs.theme.no-network-assets @inferred @source:docs/stylesheets/extra.css#:root
  Scenario: The docs theme loads no fonts or assets from the network
    Given the reader's browser has no internet access beyond the docs host
    When a reader opens any docs page
    Then text renders in the operating system's own sans-serif font
    And code renders in the operating system's own monospace font
    And no web font, CDN or analytics request is made by the theme

  @id:docs.documentation-site.site-theme.003 @rule:docs.theme.legible-in-both-schemes @inferred @source:docs/stylesheets/extra.css#[data-md-color-scheme="slate"]
  Scenario Outline: Links stay legible in both the light and the dark scheme
    Given the reader's system prefers the <scheme> colour scheme
    When a reader opens a docs page
    Then the page background is <background>
    And links are shown in <link_colour>

    Examples:
      | scheme | background                                  | link_colour        |
      | light  | the default white ground                    | dark amber #d97706 |
      | dark   | deep navy #0b0f1a, header and tabs included | amber #fbbf24      |

  @id:docs.documentation-site.site-theme.004 @rule:docs.theme.no-borrowed-logo @inferred @source:docs/stylesheets/extra.css#.md-header__button.md-logo
  Scenario: The docs header shows no stand-in logo
    Given the project has not configured a logo
    When a reader opens any docs page
    Then the header shows the site title without a logo icon
    And the navigation drawer title shows no logo icon

  @id:docs.documentation-site.site-theme.005 @rule:docs.theme.wide-tables-readable-on-narrow-screens @inferred @source:docs/stylesheets/extra.css#.md-typeset table:not([class]) @questionable
  Scenario: A table wider than the screen is cut off rather than scrollable
    Given a docs page contains a table wider than the reader's screen
    When a reader opens that page on a narrow screen
    Then the table is shown as a rounded, bordered card
    And the columns beyond the card's edge are cut off and cannot be scrolled into view
