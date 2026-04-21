// multi.go contains functions that exceed the positional-param budget.
package fixture

func TwoArgs(x int, y int) {}

func ThreeArgs(a, b, c string) {}

type Host struct{}

func (h Host) MethodTwoArgs(x, y int) {}
