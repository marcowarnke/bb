from __future__ import annotations

from .. import bitbucket, config, git, output
from ..bitbucket import Client
from ..cli import Command, Error, Opts, maximum_args, minimum_args
from ..output import add_json_flags, pick
from .common import arg_or
from .resolve import Repo, client_for, parse_repo_arg, repo_client


def new_repo_cmd() -> Command:
    cmd = Command("repo <command>", short="Manage repositories")
    cmd.add(new_repo_view_cmd(), new_repo_clone_cmd())
    return cmd


def new_repo_view_cmd() -> Command:
    def run(o: Opts) -> None:
        r, c = repo_client(arg_or(o))
        repo = c.get_repo(r.project, r.slug)
        if o.web:
            output.open_browser(bitbucket.web_url(repo))
            return
        try:
            default = c.default_branch(r.project, r.slug)
        except Error:
            default = ""
        if exp.enabled():
            origin = repo.get("origin")
            exp.write(
                pick(
                    {
                        "id": repo.get("id", 0),
                        "name": repo.get("name", ""),
                        "slug": repo.get("slug", ""),
                        "project": (repo.get("project") or {}).get("key", ""),
                        "nameWithOwner": bitbucket.full_name(repo),
                        "description": repo.get("description", ""),
                        "url": bitbucket.web_url(repo),
                        "sshUrl": bitbucket.clone_url(repo, "ssh"),
                        "httpUrl": bitbucket.clone_url(repo, "http"),
                        "defaultBranchRef": {"name": default},
                        "isPrivate": not repo.get("public", False),
                        "isFork": origin is not None,
                        "parent": bitbucket.full_name(origin) if origin is not None else None,
                    },
                    exp.fields,
                )
            )
            return
        output.out(f"name:\t{bitbucket.full_name(repo)}")
        output.out(f"description:\t{repo.get('description', '')}")
        output.out(f"default branch:\t{default}")
        output.out(f"url:\t{bitbucket.web_url(repo)}")
        output.out(f"clone:\t{bitbucket.clone_url(repo, 'ssh')} {bitbucket.clone_url(repo, 'http')}")
        readme = fetch_readme(c, r)
        if readme:
            output.out("--")
            output.out(readme)

    cmd = Command(
        "view [<repository>]",
        short="View a repository",
        long="Display the description and the README of a Bitbucket repository.\n\n"
        "With no argument, the repository for the current directory is displayed.",
        args=maximum_args(1),
        run=run,
    )
    cmd.flag("web", "w", kind="bool", help="Open a repository in the browser")
    exp = add_json_flags(
        cmd,
        [
            "defaultBranchRef",
            "description",
            "httpUrl",
            "id",
            "isFork",
            "isPrivate",
            "name",
            "nameWithOwner",
            "parent",
            "project",
            "slug",
            "sshUrl",
            "url",
        ],
    )
    return cmd


def fetch_readme(c: Client, r: Repo) -> str:
    for name in ("README.md", "README", "README.txt", "readme.md"):
        try:
            return c.raw("GET", r.path() + "/raw/" + name, None, [("Accept", "text/plain")]).text().rstrip("\n")
        except Error:
            continue
    return ""


def new_repo_clone_cmd() -> Command:
    def run(o: Opts) -> None:
        r = parse_repo_arg(o.args[0])
        c = client_for(r.host)
        repo = c.get_repo(r.project, r.slug)
        proto = "http"
        try:
            _, h = config.Config.load().resolve(r.host)
            if h.git_protocol == "ssh":
                proto = "ssh"
        except Error:
            pass
        u = bitbucket.clone_url(repo, proto)
        if not u:
            raise Error(f"no {proto} clone URL for {bitbucket.full_name(repo)}")
        git.passthrough("clone", u, *o.args[1:])

    return Command(
        "clone <repository> [<directory>] [-- <gitflags>...]",
        short="Clone a repository locally",
        long="""Clone a Bitbucket repository locally. Pass additional git clone flags after "--".

The protocol follows the host's git_protocol setting (bb auth login -p ssh|https).""",
        args=minimum_args(1),
        run=run,
    )
