# Classifies a self-service permission request into one of:
#   instant  - applied automatically
#   flagged  - applied at once, then checked by the reviewer model
#   approval - waits for an approver in the console
#   never    - refused
package agentbox.permissions

import rego.v1

default class := "approval"

class := "never" if never
else := "instant" if instant
else := "flagged" if flagged
else := "approval"

host_match(pattern, host) if glob.match(lower(pattern), ["."], lower(host))

# --- egress:<host> ---
never if {
	input.kind == "egress"
	some p in input.catalog.egress.never
	host_match(p, input.value)
}

instant if {
	input.kind == "egress"
	some p in input.catalog.egress.auto
	host_match(p, input.value)
}

flagged if {
	input.kind == "egress"
	some p in input.catalog.egress.flagged
	host_match(p, input.value)
}

# --- image:<name> ---
never if {
	input.kind == "image"
	not input.catalog.images[input.value]
}

instant if {
	input.kind == "image"
	input.value in input.group.images
}

# --- tier:<n> ---
never if {
	input.kind == "tier"
	to_number(input.value) > input.group.tier_cap
}

instant if {
	input.kind == "tier"
	to_number(input.value) <= input.user.tier
}

# --- port:<n>: raw TCP tunnels are allowed but flagged ---
never if {
	input.kind == "port"
	to_number(input.value) in {4000, 3128, 53}
}

flagged if input.kind == "port"

# --- mount:<path>: project roots are flagged, anything else needs approval ---
never if {
	input.kind == "mount"
	some p in ["/", "/etc", "/root", "/home", "C:\\", "C:\\Windows"]
	input.value == p
}

flagged if {
	input.kind == "mount"
	some p in input.catalog.project_roots
	glob.match(p, ["/", "\\"], input.value)
}

# --- rules an approver turned into automatic ones ---
instant if {
	some r in input.auto_rules
	glob.match(r, [], input.permission)
}

reason := sprintf("%s for %s", [class, input.permission])

# --- model:<name>: only models the platform serves (the catalog) can be asked for.
# One the group already allows needs no request; another one is for an approver.
never if {
	input.kind == "model"
	not input.value in input.catalog.models
}

instant if {
	input.kind == "model"
	input.value in input.group.models
}
