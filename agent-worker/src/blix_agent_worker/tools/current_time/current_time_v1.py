from datetime import datetime
from zoneinfo import ZoneInfo, ZoneInfoNotFoundError

from pydantic import ValidationError

from blix_agent_worker.models.common import (
    ContractError,
    ContractModel,
    NonEmptyText,
    validation_error_details,
)
from blix_agent_worker.models.tool import (
    ToolCall,
    ToolDefinition,
    ToolExecutionMode,
    ToolImpact,
    ToolResult,
    ToolResultStatus,
)


class CurrentTimeV1Input(ContractModel):
    timezone: NonEmptyText = "UTC"


class CurrentTimeV1Output(ContractModel):
    timezone: NonEmptyText
    local_time: datetime
    utc_offset_seconds: int


class CurrentTimeV1Tool:
    definition = ToolDefinition(
        id="current_time_v1",
        description="Return the current date and time in an IANA timezone.",
        input_schema=CurrentTimeV1Input.model_json_schema(mode="validation"),
        output_schema=CurrentTimeV1Output.model_json_schema(mode="serialization"),
        impact=ToolImpact.READ_ONLY,
        execution_mode=ToolExecutionMode.INLINE,
    )

    async def execute(self, call: ToolCall) -> ToolResult:
        try:
            tool_input = CurrentTimeV1Input.model_validate(call.arguments)
        except ValidationError as error:
            return ToolResult(
                call_id=call.call_id,
                status=ToolResultStatus.FAILED,
                error=ContractError(
                    code="invalid_arguments",
                    message="The tool arguments are invalid.",
                    details=validation_error_details(error),
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
        output = CurrentTimeV1Output(
            timezone=tool_input.timezone,
            local_time=local_time,
            utc_offset_seconds=int(utc_offset.total_seconds()) if utc_offset is not None else 0,
        )
        return ToolResult(
            call_id=call.call_id,
            status=ToolResultStatus.SUCCEEDED,
            output=output.model_dump(mode="json"),
        )
