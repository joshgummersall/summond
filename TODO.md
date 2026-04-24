# TODO

- [x] `summond list --json` — highest impact; enables agents to enumerate and filter jobs reliably without parsing table output
- [x] `summond env set KEY=VALUE` / `env get KEY` for manipulating env vars instead of editing file
- [x] `summond env remove KEY` to clean up unused env vars
- [x] Document exit codes in help text or a man page — currently only `exec` exit semantics are explained
- [ ] Consider renaming `agent` to `user` for commands, and `daemon` to `system`, as they are a bit more clear
- [ ] `--dry-run` on `remove` and `apply --prune` — safe testing of automation scripts before destructive operations
