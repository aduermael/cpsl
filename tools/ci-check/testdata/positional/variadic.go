// variadic.go verifies the final-variadic exemption.
package fixture

func VariadicOnly(args ...string) {}

func OneAndVariadic(x int, args ...string) {}
