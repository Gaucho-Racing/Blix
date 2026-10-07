import re
from collections.abc import Sequence

from blix_agent_worker.agents.tool_agent import normalize_url


def validate_citations(report: str, sources: Sequence[tuple[int, str]]) -> None:
    source_indices = [index for index, _ in sources]
    if source_indices != list(range(1, len(sources) + 1)):
        raise ValueError("source indices must be sequential and start at 1")
    source_urls = [normalize_url(url) for _, url in sources]
    if len(source_urls) != len(set(source_urls)):
        raise ValueError("source URLs must be unique")
    citations = {int(value) for value in re.findall(r"\[(\d+)]", report)}
    if citations.difference(source_indices):
        raise ValueError("report contains citations that are missing from sources")
    if set(source_indices).difference(citations):
        raise ValueError("every indexed source must be cited in the report")


def validate_sources_retrieved(urls: Sequence[str], retrieved: frozenset[str], tool_names: str) -> None:
    unretrieved = [url for url in urls if normalize_url(url) not in retrieved]
    if unretrieved:
        raise ValueError(
            f"sources must be URLs returned by {tool_names} in this task; "
            f"these were never retrieved: {', '.join(unretrieved)}"
        )
