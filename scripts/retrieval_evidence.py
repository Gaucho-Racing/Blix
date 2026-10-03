from __future__ import annotations

import hashlib
import math
from pathlib import Path
import re
import subprocess


CONTEXT_CHUNKS = 5
CONTEXT_BYTES = 16000
CONTEXT_IMAGES = 3


def validate_suite(suite: dict, pages: dict[str, list[str]] | None = None) -> None:
    if suite.get('version') != 2 or not suite.get('questions'):
        raise ValueError('Expected a nonempty version 2 evidence suite')
    documents = {item['key']: item for item in suite['documents']}
    if len(documents) != len(suite['documents']):
        raise ValueError('Duplicate fixture document key')
    question_ids = set()
    for question in suite['questions']:
        label = question['id']
        if label in question_ids or question['document'] not in documents:
            raise ValueError(f'Invalid question identity: {label}')
        question_ids.add(label)
        for pattern in question['answer_patterns']:
            re.compile(pattern)
        groups = question['evidence_groups']
        if bool(groups) == question['expect_abstention']:
            raise ValueError(f'Question {label} must have evidence exactly when answerable')
        group_ids = set()
        has_visual = False
        for group in groups:
            if not group['id'] or group['id'] in group_ids or not group['claim'] or not group['alternatives']:
                raise ValueError(f'Invalid evidence group: {label}')
            group_ids.add(group['id'])
            for alternative in group['alternatives']:
                if not alternative['spans']:
                    raise ValueError(f'Empty evidence alternative: {label}')
                for span in alternative['spans']:
                    doc, page = span['document'], span['page']
                    if doc not in documents or type(page) is not int or page < 1 or (pages is not None and page > len(pages[doc])):
                        raise ValueError(f'Invalid evidence location: {label}')
                    if span['kind'] == 'text':
                        start, end = span['start_offset'], span['end_offset']
                        if span['offset_unit'] != 'utf8_bytes' or type(start) is not int or type(end) is not int or start < 0 or end <= start or not span['quote'].strip() or len(span['quote'].encode()) != end - start:
                            raise ValueError(f'Invalid evidence offsets: {label}')
                        if pages is not None and pages[doc][page - 1].encode()[start:end] != span['quote'].encode():
                            raise ValueError(f'Evidence quote/offset mismatch: {label}, {doc} page {page}')
                    elif span['kind'] == 'visual':
                        has_visual = True
                        region = span['region']
                        if len(region) != 4 or not all(type(v) in (float, int) and math.isfinite(v) and 0 <= v <= 1 for v in region) or region[0] >= region[2] or region[1] >= region[3] or not span['label'] or not span['description_patterns']:
                            raise ValueError(f'Invalid visual evidence: {label}')
                        for pattern in span['description_patterns']:
                            re.compile(pattern)
                    else:
                        raise ValueError(f'Unknown evidence kind: {label}')
        if has_visual != question['requires_image']:
            raise ValueError(f'Visual evidence/image requirement mismatch: {label}')


def validate_corpus(suite: dict, corpus: Path) -> None:
    pages = {}
    for document in suite['documents']:
        path = corpus / document['filename']
        if hashlib.sha256(path.read_bytes()).hexdigest() != document['sha256']:
            raise ValueError(f"PDF version mismatch: {document['filename']}")
        result = subprocess.run(['pdftotext', '-layout', '-enc', 'UTF-8', str(path), '-'], capture_output=True)
        if result.returncode:
            raise RuntimeError(f"Cannot extract evidence reference: {document['filename']}")
        text = result.stdout.decode().split('\f')
        pages[document['key']] = text[:-1] if text[-1] == '' else text
    validate_suite(suite, pages)


def merged_intervals(intervals: list[tuple[int, int]]) -> list[tuple[int, int]]:
    merged = []
    for start, end in sorted(intervals):
        if end <= start:
            continue
        if merged and start <= merged[-1][1]:
            merged[-1] = (merged[-1][0], max(end, merged[-1][1]))
        else:
            merged.append((start, end))
    return merged


def text_overlap(span: dict, hit: dict, documents: dict[str, str]) -> tuple[int, int] | None:
    if hit['document_id'] != documents[span['document']] or hit['pdf_page'] != span['page'] or hit['evidence_type'] != 'quoted_source' or hit.get('offset_unit') != 'utf8_bytes':
        return None
    content = hit['content'].encode()
    start, end = hit['start_offset'], hit['end_offset']
    if end - start != len(content):
        raise ValueError('Retrieved passage offsets do not match its visible content')
    left, right = max(start, span['start_offset']), min(end, span['end_offset'])
    if right <= left:
        return None
    gold = span['quote'].encode()[left - span['start_offset']:right - span['start_offset']]
    if content[left - start:right - start] != gold:
        return None
    return left, right


def visual_description_matches(span: dict, hit: dict, documents: dict[str, str]) -> bool:
    return hit['document_id'] == documents[span['document']] and hit['pdf_page'] == span['page'] and hit['evidence_type'] == 'agent_summary' and hit.get('capture_method') == 'document_vision' and all(re.search(pattern, hit['content'], re.IGNORECASE) for pattern in span['description_patterns'])


def image_covers(span: dict, image: dict) -> bool:
    if image['document'] != span['document'] or image['page'] != span['page']:
        return False
    region = image['region']
    gold = span['region']
    return region[0] <= gold[0] and region[1] <= gold[1] and region[2] >= gold[2] and region[3] >= gold[3]


def evidence_support(question: dict, hits: list[dict], documents: dict[str, str], images: list[dict] | None = None) -> dict:
    groups = []
    relevant = set()
    quoted_intervals = {}
    supporting_intervals = {}
    for hit in hits:
        if hit['evidence_type'] == 'quoted_source' and hit.get('offset_unit') == 'utf8_bytes':
            identity = (hit['document_id'], hit['pdf_page'])
            if hit['end_offset'] - hit['start_offset'] != len(hit['content'].encode()):
                raise ValueError('Retrieved passage offsets do not match its visible content')
            quoted_intervals.setdefault(identity, []).append((hit['start_offset'], hit['end_offset']))
    for group in question['evidence_groups']:
        alternatives = []
        for alternative in group['alternatives']:
            spans = []
            for span in alternative['spans']:
                if span['kind'] == 'text':
                    intervals = []
                    for index, hit in enumerate(hits):
                        overlap = text_overlap(span, hit, documents)
                        if overlap is not None:
                            intervals.append(overlap)
                            relevant.add(index)
                            identity = (hit['document_id'], hit['pdf_page'])
                            supporting_intervals.setdefault(identity, []).append(overlap)
                    fraction = sum(end - start for start, end in merged_intervals(intervals)) / (span['end_offset'] - span['start_offset'])
                    covered = fraction == 1
                    basis = 'quoted_text'
                else:
                    matches = [index for index, hit in enumerate(hits) if visual_description_matches(span, hit, documents)]
                    relevant.update(matches)
                    covered = bool(matches) if images is None else any(image_covers(span, image) for image in images)
                    fraction = int(covered)
                    basis = 'visual_description_proxy' if images is None else 'original_image_region'
                spans.append({'document': span['document'], 'page': span['page'], 'kind': span['kind'], 'coverage': fraction, 'covered': covered, 'basis': basis})
            alternatives.append({'covered': all(span['covered'] for span in spans), 'coverage': sum(span['coverage'] for span in spans) / len(spans), 'spans': spans})
        groups.append({'id': group['id'], 'claim': group['claim'], 'covered': any(alt['covered'] for alt in alternatives), 'best_partial_coverage': max(alt['coverage'] for alt in alternatives), 'alternatives': alternatives})
    count = len(groups)
    quoted_bytes = sum(end - start for intervals in quoted_intervals.values() for start, end in merged_intervals(intervals))
    supporting_bytes = sum(end - start for intervals in supporting_intervals.values() for start, end in merged_intervals(intervals))
    return {
        'required_groups': count,
        'covered_groups': sum(group['covered'] for group in groups),
        'evidence_recall': sum(group['covered'] for group in groups) / count if count else None,
        'complete_support': all(group['covered'] for group in groups) if count else None,
        'chunk_precision': len(relevant) / len(hits) if count and hits else (0 if count else None),
        'text_evidence_byte_precision': supporting_bytes / quoted_bytes if count and quoted_bytes else None,
        'returned_chunks': len(hits),
        'relevant_chunk_indexes': sorted(relevant),
        'missing_groups': [group['id'] for group in groups if not group['covered']],
        'groups': groups,
    }


def answer_context(result: dict, documents: dict[str, str], *, max_chunks: int = CONTEXT_CHUNKS, max_bytes: int = CONTEXT_BYTES, max_images: int = CONTEXT_IMAGES) -> dict:
    remaining = max_bytes
    passages = []
    for hit in result['retrieval']['results'][:max_chunks]:
        if remaining <= 0:
            break
        visible = hit['content'].encode()[:remaining].decode('utf-8', errors='ignore')
        if not visible:
            continue
        passage = {**hit, 'content': visible, 'end_offset': hit['start_offset'] + len(visible.encode()), 'truncated': hit.get('truncated', False) or visible != hit['content']}
        passages.append(passage)
        remaining -= len(visible.encode())
    id_to_key = {value: key for key, value in documents.items()}
    images = []
    if result['requires_image']:
        seen = set()
        for hit in passages:
            identity = (hit['document_id'], hit['pdf_page'])
            if identity in seen or len(images) >= max_images:
                continue
            seen.add(identity)
            images.append({'document': id_to_key[hit['document_id']], 'page': hit['pdf_page'], 'region': [0, 0, 1, 1]})
    return {'passages': passages, 'images': images, 'limits': {'chunks': max_chunks, 'text_bytes': max_bytes, 'images': max_images}, 'text_bytes': max_bytes - remaining}


def score_result(result: dict, documents: dict[str, str]) -> None:
    hits = result['retrieval']['results']
    result['evidence_at_k'] = {str(k): evidence_support(result, hits[:k], documents) for k in (1, 5, 10)}
    result['returned_evidence'] = evidence_support(result, hits, documents)
    rank = next((index for index in range(1, len(hits) + 1) if evidence_support(result, hits[:index], documents)['complete_support']), None)
    result['complete_support_reciprocal_rank'] = 1 / rank if rank else 0
    result['planned_answer_context'] = answer_context(result, documents)
    planned = result['planned_answer_context']
    result['planned_context_support'] = evidence_support(result, planned['passages'], documents, planned['images'])
