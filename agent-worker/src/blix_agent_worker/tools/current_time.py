from datetime import datetime
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from pydantic import ValidationError

from blix_agent_worker.common import ContractError, ContractModel, NonEmptyText
from blix_agent_worker.tools.main import (
    ToolCall,
    ToolDefinition,
    ToolExecutionMode,
    ToolImpact,
    ToolResult,
    ToolResultStatus,
)


class CurrentTimeInput(ContractModel):
    timezone: NonEmptyText = "UTC"


class CurrentTimeOutput(ContractModel):
    timezone: NonEmptyText
    local_time: datetime
    utc_offset_seconds: int


class CurrentTimeTool:
    definition = ToolDefinition(
        name="current_time",
        description="Return the current date and time in an IANA timezone.",
        input_schema=CurrentTimeInput.model_json_schema(mode="validation"),
        output_schema=CurrentTimeOutput.model_json_schema(mode="serialization"),
        impact=ToolImpact.READ_ONLY,
        execution_mode=ToolExecutionMode.INLINE,
        timeout_seconds=5,
    )

    async def execute(self, call: ToolCall) -> ToolResult:
        if call.name != self.definition.name or call.version != self.definition.version:
            return ToolResult(
                call_id=call.call_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="tool_mismatch",
                    message="The tool call does not match this tool definition.",
                    details={
                        "expected_name": self.definition.name,
                        "expected_version": self.definition.version,
                        "actual_name": call.name,
                        "actual_version": call.version,
                    },
                ),
            )

        try:
            tool_input = CurrentTimeInput.model_validate(call.arguments)
        except ValidationError as error:
            validation_errors = [
                {
                    "location": ".".join(str(part) for part in item["loc"]),
                    "message": item["msg"],
                    "type": item["type"],
                }
                for item in error.errors(include_url=False, include_context=False, include_input=False)
            ]
            return ToolResult(
                call_id=call.call_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="invalid_arguments",
                    message="The tool arguments are invalid.",
                    details={"errors": validation_errors},
                ),
            )

        try:
            timezone = ZoneInfo(tool_input.timezone)
        except ZoneInfoNotFoundError:
            return ToolResult(
                call_id=call.call_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="timezone_not_found",
                    message="The requested IANA timezone was not found.",
                    details={"timezone": tool_input.timezone},
                ),
            )

        local_time = datetime.now(timezone)
        utc_offset = local_time.utcoffset()
        output = CurrentTimeOutput(
            timezone=tool_input.timezone,
            local_time=local_time,
            utc_offset_seconds=int(utc_offset.total_seconds()) if utc_offset is not None else 0,
        )
        return ToolResult(
            call_id=call.call_id,
            status=ToolResultStatus.SUCCEEDED,
            output=output.model_dump(mode="json"),
        )
