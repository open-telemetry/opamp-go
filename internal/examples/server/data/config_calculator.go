package data

type ConfigCalculator interface {
	Calculate(agent *Agent, configBody string) string
	AgentsChanged(agents map[InstanceId]*Agent) map[InstanceId]bool
}
