# Idea: branch matching

Status: specified

## Summary

I do not want to be forced to always type the full branch name. `wtree path harden` should do the same as
`wtree path feat/harden-loops`, if there is only one matching branch. If there are multiple matching branches,
the user should be prompted to choose one. multiple matching branches,
the user should be prompted to choose one.

If `wtree path` is used or an option that is usually used by other programs like --json, it should not prompt the customer, but fail with an error.