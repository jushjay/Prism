# Ant Design Pro Frontend Design

## Product Direction

This branch is a new operations console for Prism. It is not a visual continuation of the previous React dashboard. The backend APIs remain usable, but the frontend treats them as data contracts for a new Ant Design Pro application.

## Design Principles

- Use Ant Design Pro as the primary framework: `ProLayout`, `PageContainer`, `ProCard`, `ProTable`, `ProDescriptions`, `StatisticCard`, and `LoginFormPage`.
- Optimize for repeated operations work: dense tables, predictable filters, clear status tags, and direct actions.
- Avoid hero-style marketing composition. The first viewport should show operational data and controls.
- Prefer neutral light UI: white panels, gray page background, restrained shadows, and status colors only where they encode state.
- Use tables for inventories and event logs. Use descriptions for read-only configuration and cards only for grouped operational panels.
- Keep backend API compatibility unless the interface blocks an efficient workflow.

## Information Architecture

- Overview: system health, usage baseline, runtime policy, and account-pool status.
- Accounts: account inventory, provider split, quota windows, OAuth and custom-provider onboarding.
- Usage: trend analysis, aggregate counters, filterable event table.
- Models: account-bound catalog, dynamic/manual model tables, mapping table, manual model and mapping forms.
- Security: firewall state, access sources, denied traffic, whitelist and blacklist rules.
- Settings: read-only runtime configuration grouped by domain.
- Examples: API request snippets and current access parameters.

## Visual System

- Layout: light `ProLayout` with top header and left navigation.
- Density: compact but readable, suitable for admin workflows.
- Radius: small, stable radius around panels and controls; avoid oversized rounded cards.
- Palette: neutral gray background, white content panels, blue only as primary action/status accent.
- Motion: lightweight section entrance and no decorative motion.

## Interaction Model

- Page data loads on demand from current route.
- Primary action for each page is exposed in the page toolbar or section header.
- Tables avoid hidden actions where possible; edit/delete/refresh controls stay visible in operation columns.
- Modals are used only for forms that create or edit backend resources.
