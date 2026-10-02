import base64
import hashlib
import os

from blix_agent_worker.clients.knowledge import KnowledgeClient, KnowledgeClientError
from blix_agent_worker.models.llm import DocumentImageReference, ModelMessageRole, ModelRequest
from blix_agent_worker.models.tool import ToolResult, ToolResultStatus


async def hydrate_document_images(
    request: ModelRequest, client: KnowledgeClient, run_id: str, session_id: str
) -> ModelRequest:
    references = {image.uri: image for image in request.images}
    calls = {call.ref: call for message in request.messages for call in message.call_requests}
    for message in request.messages:
        if message.role is not ModelMessageRole.CALL_OUTPUT:
            continue
        call = calls.get(message.call_request_ref)
        if (call is None or call.callable_id != "knowledge_document_search_v1"
                or call.arguments.get("operation") != "read_page_image"):
            continue
        result = ToolResult.model_validate_json(message.content)
        if result.status is not ToolResultStatus.SUCCEEDED or not result.output.get("image_reference"):
            continue
        if result.run_id != run_id or result.parent_session_id != session_id:
            raise ValueError("image receipt does not belong to this execution")
        image = DocumentImageReference.model_validate(result.output["image_reference"])
        if call.arguments.get("document_id") != image.document_id or call.arguments.get("page") != image.page:
            raise ValueError("image receipt does not match the requested document page")
        references[image.uri] = image
    if request.continuation is not None:
        for item in request.continuation.state.get("input_items", []):
            if not isinstance(item, dict) or not isinstance(item.get("content"), list):
                continue
            for part in item["content"]:
                if not isinstance(part, dict) or part.get("type") != "input_image":
                    continue
                uri = part.get("image_url", "")
                if not isinstance(uri, str) or not uri.startswith("blix-document://"):
                    raise ValueError("saved document image reference is invalid")
                document_id, page, sha256 = uri.removeprefix("blix-document://").split("/")
                image = DocumentImageReference(document_id=document_id, page=int(page), sha256=sha256)
                references[image.uri] = image
    if len(references) > 20:
        raise ValueError("a model request may inspect at most 20 document pages")
    data = {}
    total_bytes = 0
    for uri, image in references.items():
        result = await client.request(
            os.environ["KNOWLEDGE_URL"], "GET", f"documents/{image.document_id}/pages/{image.page}/image",
            params={"run_id": run_id, "session_id": session_id},
        )
        returned_identity = (result.get("document_id"), result.get("page"), result.get("sha256"))
        if returned_identity != (image.document_id, image.page, image.sha256):
            raise KnowledgeClientError("document_image_changed", "The document page image changed.")
        url = result.get("data_url")
        if not isinstance(url, str) or not url.startswith("data:image/jpeg;base64,") or len(url) > 12 * 1024 * 1024:
            raise KnowledgeClientError("invalid_document_image", "The knowledge service returned an invalid page image.")
        try:
            content = base64.b64decode(url.split(",", 1)[1], validate=True)
        except ValueError as error:
            raise KnowledgeClientError("invalid_document_image", "The page image encoding is invalid.") from error
        if hashlib.sha256(content).hexdigest() != image.sha256:
            raise KnowledgeClientError("document_image_changed", "The document page image changed.")
        total_bytes += len(content)
        if total_bytes > 24 * 1024 * 1024:
            raise KnowledgeClientError("document_images_too_large", "Inspect fewer document pages in this question.")
        data[uri] = url
    return request.model_copy(update={"images": tuple(references.values()), "image_data": data})
