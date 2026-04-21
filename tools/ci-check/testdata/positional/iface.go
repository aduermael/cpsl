// iface.go verifies that interface method declarations are not scanned.
package fixture

type Worker interface {
	Do(x int, y int, z int)
}
