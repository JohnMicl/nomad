---
layout: "docs"
page_title: "Commands: alloc"
sidebar_current: "docs-commands-alloc"
description: >
  The alloc command is used to interact with allocations.
---

# Command: alloc

The `alloc` command is used to interact with allocations.

## Usage

Usage: `nomad alloc <subcommand> [options]`

Run `nomad alloc <subcommand> -h` for help on that subcommand. The following
subcommands are available:

- [`alloc exec`][exec] - Run a command in a running allocation
- [`alloc fs`][fs] - Inspect the contents of an allocation directory
- [`alloc logs`][logs] - Streams the logs of a task
- [`alloc restart`][restart] - Restart a running allocation or task
- [`alloc signal`][signal] - Signal a running allocation
- [`alloc status`][status] - Display allocation status information and metadata
- [`alloc stop`][stop] - Stop and reschedule a running allocation

[exec]: /docs/commands/alloc/exec.html "Run a command in a running allocation"
[fs]: /docs/commands/alloc/fs.html "Inspect the contents of an allocation directory"
[logs]: /docs/commands/alloc/logs.html "Streams the logs of a task"
[restart]: /docs/commands/alloc/restart.html "Restart a running allocation or task"
[signal]: /docs/commands/alloc/signal.html "Signal a running allocation"
[status]: /docs/commands/alloc/status.html "Display allocation status information and metadata"
[stop]: /docs/commands/alloc/stop.html "Stop and reschedule a running allocation"
