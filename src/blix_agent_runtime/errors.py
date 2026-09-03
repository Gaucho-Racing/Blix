class AgentRuntimeError(Exception):
    pass


class ConfigurationError(AgentRuntimeError):
    pass


class ModelGatewayError(AgentRuntimeError):
    pass


class ToolConfigurationError(AgentRuntimeError):
    pass
