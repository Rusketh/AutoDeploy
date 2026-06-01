package unattend

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Validate parses a generated unattend.xml and checks the structural rules
// that Windows Setup enforces but a string-concatenation generator can
// silently violate. It is NOT a full XSD validation; it targets the
// failure classes we can actually hit:
//
//   - well-formed XML and the expected root element;
//   - child-element ORDER within elements whose schema fixes a sequence
//     (e.g. UserData requires ProductKey before AcceptEula -- the wrong
//     order makes Setup abort at specialize with "the computer restarted
//     unexpectedly").
//
// Unknown elements and unknown children are ignored, so the generator can
// grow without the validator needing to know every element -- it only
// enforces the orderings it knows, which is where the bugs are.
func Validate(xmlBytes []byte) error {
	root, err := parseNode(xmlBytes)
	if err != nil {
		return err
	}
	if root.name != "unattend" {
		return fmt.Errorf("unattend: root element is %q, want \"unattend\"", root.name)
	}
	var problems []string
	walkOrder(root, &problems)
	if len(problems) > 0 {
		return fmt.Errorf("unattend: schema order violations:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// orderedChildren maps an element (by local name) to the canonical order
// of those of its children whose sequence the schema fixes. Children not
// listed here are unconstrained and ignored by the check. Add entries as
// new ordering-sensitive sections are emitted.
var orderedChildren = map[string][]string{
	// Microsoft-Windows-Setup component (windowsPE pass).
	"component": {"DiskConfiguration", "ImageInstall", "RunSynchronous", "UserData"},
	// UserData: ProductKey MUST precede AcceptEula. This is the ordering
	// whose violation aborted Setup at specialize.
	"UserData": {"ProductKey", "AcceptEula", "FullName", "Organization"},
	// A created partition is carved (CreatePartitions) before it is
	// formatted (ModifyPartitions).
	"Disk": {"CreatePartitions", "ModifyPartitions"},
	"OOBE": {"HideEULAPage", "HideOEMRegistrationScreen", "HideOnlineAccountScreens",
		"HideWirelessSetupInOOBE", "ProtectYourPC", "SkipMachineOOBE", "SkipUserOOBE"},
	// UnattendedJoin Identification: Credentials before JoinDomain before
	// MachineObjectOU.
	"Identification": {"Credentials", "JoinDomain", "JoinWorkgroup", "MachineObjectOU", "DebugJoin"},
	// CredentialsType: Domain, Password, Username (in that order). Username
	// before Password aborts Setup at specialize (0x8030000b).
	"Credentials": {"Domain", "Password", "Username"},
}

// walkOrder checks each element's known-ordered children appear in the
// canonical order, recursively.
func walkOrder(n *node, problems *[]string) {
	if want, ok := orderedChildren[n.name]; ok {
		rank := map[string]int{}
		for i, name := range want {
			rank[name] = i
		}
		last := -1
		lastName := ""
		for _, c := range n.children {
			r, known := rank[c.name]
			if !known {
				continue
			}
			if r < last {
				*problems = append(*problems,
					fmt.Sprintf("in <%s>: <%s> must come before <%s>", n.name, c.name, lastName))
			} else {
				last, lastName = r, c.name
			}
		}
	}
	for _, c := range n.children {
		walkOrder(c, problems)
	}
}

// node is a minimal ordered element tree (children in document order).
type node struct {
	name     string
	children []*node
}

// parseNode streams the XML and builds the element tree, preserving child
// order. Attributes and text are discarded -- only structure matters here.
func parseNode(xmlBytes []byte) (*node, error) {
	dec := xml.NewDecoder(strings.NewReader(string(xmlBytes)))
	var stack []*node
	var root *node
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			return nil, fmt.Errorf("unattend: malformed XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name.Local}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("unattend: no elements found")
	}
	return root, nil
}
