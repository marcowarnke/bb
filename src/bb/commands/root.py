"""The bb command tree, mirroring gh's UX."""

from __future__ import annotations

import os
import sys
import traceback

from .. import __version__, bitbucket, cli, config, output
from ..cli import Command


def new_root() -> Command:
    from . import api, auth, browse, completion, pr, repo, skills, status

    root = Command(
        "bb <command> <subcommand> [flags]",
        short="Bitbucket Data Center CLI",
        long="""Work seamlessly with Bitbucket Data Center from the command line.

Modelled after the GitHub CLI (gh): same commands, flags and output conventions.""",
        example="""  $ bb pr list
  $ bb pr create --fill --reviewer alice
  $ bb pr diff 42
  $ bb pr comment 42 --body "LGTM" --path src/main.go --line 10
  $ bb pr review 42 --approve""",
        version=__version__,
    )
    root.add(
        auth.new_auth_cmd(),
        browse.new_browse_cmd(),
        pr.new_pr_cmd(),
        repo.new_repo_cmd(),
        status.new_status_cmd(),
        api.new_api_cmd(),
        skills.new_skills_cmd(),
    )
    cli.add_completion_command(root)
    cli.add_help_command(root)
    completion.register_completions(root)
    return root


def _map_error(err: BaseException) -> int:
    if isinstance(err, (cli.Error, OSError)):
        output.err(str(err))
    else:
        output.err(f"bb: unexpected error: {type(err).__name__}: {err}")
        if os.environ.get("BB_DEBUG"):
            traceback.print_exception(err, file=output.io.stderr)
    api = cli.cause(err, bitbucket.APIError)
    if (api is not None and api.status == 401) or cli.cause(err, config.NoHostError) is not None:
        return 4
    return 1


def run(argv: list[str] | None = None) -> int:
    """Run the CLI and return the process exit code (gh: 1 error, 4 auth, 8 pending checks)."""
    return cli.run_main(new_root(), argv, _map_error)


def main() -> None:
    output.setup_streams()
    sys.exit(run())
