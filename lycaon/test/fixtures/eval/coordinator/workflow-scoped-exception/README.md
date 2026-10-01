# Scoped exception and atomic release

Resolve the two production components and their atomic release group.

api disposition and web disposition record component, revision, disposition and exception. A passed check yields ready/none. A failed check yields excepted/the exception ID only when an active exception matches release, component, revision, environment and check exactly; otherwise blocked/none. Exceptions do not transfer between components or revisions.
select action chooses hold if either component is blocked; otherwise release. The group is atomic: an individually ready or excepted component does not release while its sibling blocks the group.
The chosen action phase records action, api and web dispositions. release index records api and web as released or held.

All supplied files are immutable. Deliver records through this workflow; do not create files. Cite the source records used for every terminal verdict.
