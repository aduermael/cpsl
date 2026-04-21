// ok.go is a baseline fixture where every function passes the rule.
package fixture

func None()     {}
func One(x int) {}

type Other struct{}

func (o Other) Method(x int) {}
