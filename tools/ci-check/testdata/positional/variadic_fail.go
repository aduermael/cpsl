// variadic_fail.go verifies that multiple regular params plus variadic
// still count as a violation.
package fixture

func TwoAndVariadic(x, y int, args ...string) {}
