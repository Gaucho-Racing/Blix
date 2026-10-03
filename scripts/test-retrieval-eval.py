import copy
import importlib.util
from pathlib import Path
import unittest
import tempfile

from retrieval_evidence import answer_context, evidence_support, score_result, validate_suite

spec = importlib.util.spec_from_file_location('retrieval_eval', Path(__file__).with_name('eval-retrieval.py'))
eval_module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(eval_module)
DOCUMENTS = {'sae': 'sae-id', 'gr25': 'gr25-id'}


def span(quote='350 mm', start=0, page=24, document='sae'):
    return {'kind': 'text', 'document': document, 'page': page, 'quote': quote, 'start_offset': start, 'end_offset': start + len(quote.encode()), 'offset_unit': 'utf8_bytes'}


def group(*alternatives):
    return {'id': 'height', 'claim': 'Height is 350 mm', 'alternatives': [{'spans': items} for items in alternatives]}


def hit(content='350 mm', start=0, page=24, document='sae', **extra):
    return {'source_chunk_id': f'{document}-{page}-{start}', 'document_id': DOCUMENTS[document], 'pdf_page': page, 'content': content, 'start_offset': start, 'end_offset': start + len(content.encode()), 'offset_unit': 'utf8_bytes', 'evidence_type': 'quoted_source', 'capture_method': 'document_upload', **extra}


def question(groups=None, hits=None, **extra):
    return {'id': 'test', 'document': 'sae', 'query': 'Height?', 'expected_pages': [24], 'expect_abstention': False, 'requires_image': False, 'answer_patterns': ['350'], 'evidence_groups': [group([span()])] if groups is None else groups, 'retrieval': {'results': [hit()] if hits is None else hits, 'semantic_status': 'disabled', 'retrieval': 'document_lexical_v1'}, **extra}


def figure():
    return {'kind': 'visual', 'document': 'sae', 'page': 24, 'label': 'Component envelope', 'region': [.2, .5, .8, .8], 'description_patterns': ['component envelope', '350']}


class EvaluationTests(unittest.TestCase):
    def test_docker_runner_is_isolated_and_passes_only_embedding_secrets(self):
        with tempfile.TemporaryDirectory() as directory:
            temporary = Path(directory)
            values = {'EMBEDDING_MODEL': 'model', 'EMBEDDING_API_KEY': 'secret', 'LLM_API_KEY': 'other-secret', 'DATABASE_PASSWORD': 'production'}
            command = eval_module.docker_retrieval_command('eval-db', Path('/corpus with spaces'), temporary, values, 'hybrid', temporary / 'visuals.json')
            self.assertIn('container:eval-db', command)
            self.assertEqual(command[command.index('-port') + 1], '5432')
            self.assertIn('type=bind,src=/corpus with spaces,dst=/corpus,readonly', command)
            self.assertNotIn('secret', command)
            settings = temporary / 'embedding.env'
            self.assertEqual(settings.stat().st_mode & 0o777, 0o600)
            self.assertIn('EMBEDDING_API_KEY=secret', settings.read_text())
            self.assertNotIn('other-secret', settings.read_text())
            self.assertNotIn('production', settings.read_text())

    def test_docker_environment_rejects_multiline_values(self):
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(ValueError, 'single-line'):
                eval_module.docker_retrieval_command('eval-db', Path('/corpus'), Path(directory), {'EMBEDDING_API_KEY': 'bad\nDATABASE_PASSWORD=injected'}, 'hybrid', None)

    def test_correct_page_wrong_chunk_is_not_evidence(self):
        q = question(hits=[hit('Brake light', start=30)])
        eval_module.score_pages(q, DOCUMENTS)
        self.assertTrue(q['hit_at_1'])
        self.assertFalse(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['complete_support'])

    def test_same_offsets_wrong_document_or_wrong_content_fail(self):
        for wrong in [hit(document='gr25'), hit('600 mm')]:
            q = question(hits=[wrong])
            self.assertFalse(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['complete_support'])

    def test_split_span_can_be_covered_by_multiple_chunks(self):
        q = question(hits=[hit('350 '), hit('mm', start=4)])
        score = evidence_support(q, q['retrieval']['results'], DOCUMENTS)
        self.assertTrue(score['complete_support'])
        self.assertEqual(score['evidence_recall'], 1)

    def test_missing_middle_of_span_does_not_count(self):
        q = question(hits=[hit('35'), hit('mm', start=4)])
        score = evidence_support(q, q['retrieval']['results'], DOCUMENTS)
        self.assertFalse(score['complete_support'])
        self.assertAlmostEqual(score['groups'][0]['best_partial_coverage'], 4 / 6)

    def test_overlap_and_duplicate_hits_do_not_inflate_coverage(self):
        q = question(hits=[hit('350 '), hit('350 '), hit('0 ', start=2)])
        score = evidence_support(q, q['retrieval']['results'], DOCUMENTS)
        self.assertFalse(score['complete_support'])
        self.assertAlmostEqual(score['groups'][0]['best_partial_coverage'], 4 / 6)

    def test_all_spans_across_pages_are_required(self):
        q = question(groups=[group([span('local', page=12), span('upload', page=13)])], hits=[hit('local', page=12)])
        self.assertFalse(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['complete_support'])
        q['retrieval']['results'].append(hit('upload', page=13))
        self.assertTrue(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['complete_support'])

    def test_equivalent_alternative_satisfies_group(self):
        q = question(groups=[group([span(page=24)], [span('Equivalent', page=25)])], hits=[hit('Equivalent', page=25)])
        self.assertTrue(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['complete_support'])

    def test_partial_multi_group_recall_is_not_complete_support(self):
        second = group([span('upload', page=13)])
        second['id'] = 'upload'
        q = question(groups=[group([span()]), second])
        score = evidence_support(q, q['retrieval']['results'], DOCUMENTS)
        self.assertEqual(score['evidence_recall'], .5)
        self.assertEqual(score['missing_groups'], ['upload'])

    def test_precision_counts_irrelevant_chunks(self):
        q = question(hits=[hit(), hit('Brake lights', start=20)])
        self.assertEqual(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['chunk_precision'], .5)

    def test_large_chunk_and_duplicate_overlap_do_not_inflate_byte_precision(self):
        q = question(hits=[hit('350 mm' + 'x' * 94), hit('350 mm' + 'x' * 94)])
        score = evidence_support(q, q['retrieval']['results'], DOCUMENTS)
        self.assertEqual(score['chunk_precision'], 1)
        self.assertEqual(score['text_evidence_byte_precision'], .06)

    def test_committed_fixture_schema(self):
        import json
        suite = json.loads((Path(__file__).resolve().parents[1] / 'knowledge/evals/retrieval.json').read_text())
        validate_suite(suite)
        self.assertEqual(len(suite['questions']), 30)

    def test_utf8_byte_offsets_not_character_offsets(self):
        q = question(groups=[group([span('60°C', start=2)])], hits=[hit('é60°C')])
        self.assertTrue(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['complete_support'])
        q['retrieval']['results'][0]['end_offset'] -= 1
        with self.assertRaisesRegex(ValueError, 'visible content'):
            evidence_support(q, q['retrieval']['results'], DOCUMENTS)

    def test_budget_truncation_cannot_count_hidden_evidence(self):
        q = question(hits=[hit('350 mm')])
        context = answer_context(q, DOCUMENTS, max_bytes=4)
        self.assertEqual(context['passages'][0]['content'], '350 ')
        self.assertTrue(context['passages'][0]['truncated'])
        self.assertFalse(evidence_support(q, context['passages'], DOCUMENTS, context['images'])['complete_support'])

    def test_budget_preserves_utf8_boundaries(self):
        q = question(hits=[hit('é350 mm')])
        context = answer_context(q, DOCUMENTS, max_bytes=1)
        self.assertEqual(context['passages'], [])
        self.assertEqual(context['text_bytes'], 0)

    def test_k_cutoff_and_answer_context_drop_lower_ranked_evidence(self):
        q = question(hits=[hit('Noise', start=100 + i * 10) for i in range(5)] + [hit()])
        score_result(q, DOCUMENTS)
        self.assertFalse(q['evidence_at_k']['5']['complete_support'])
        self.assertTrue(q['evidence_at_k']['10']['complete_support'])
        self.assertFalse(q['planned_context_support']['complete_support'])
        self.assertAlmostEqual(q['complete_support_reciprocal_rank'], 1 / 6)

    def test_reranking_recomputes_final_context(self):
        q = question(hits=[hit('Noise', start=100 + i * 10) for i in range(5)] + [hit()])
        score_result(q, DOCUMENTS)
        q['retrieval']['results'].reverse()
        score_result(q, DOCUMENTS)
        self.assertTrue(q['planned_context_support']['complete_support'])
        self.assertEqual(q['complete_support_reciprocal_rank'], 1)

    def test_visual_description_is_only_a_discovery_proxy(self):
        q = question(groups=[group([figure()])], hits=[hit('Component envelope: 350 mm', evidence_type='agent_summary', capture_method='document_vision')], requires_image=True)
        score = evidence_support(q, q['retrieval']['results'], DOCUMENTS)
        self.assertTrue(score['complete_support'])
        self.assertEqual(score['groups'][0]['alternatives'][0]['spans'][0]['basis'], 'visual_description_proxy')
        self.assertFalse(evidence_support(q, q['retrieval']['results'], DOCUMENTS, [])['complete_support'])

    def test_wrong_figure_on_same_page_is_not_relevant(self):
        q = question(groups=[group([figure()])], hits=[hit('Tire envelope', evidence_type='agent_summary', capture_method='document_vision')], requires_image=True)
        self.assertFalse(evidence_support(q, q['retrieval']['results'], DOCUMENTS)['complete_support'])

    def test_original_image_must_cover_gold_region(self):
        q = question(groups=[group([figure()])], requires_image=True)
        for region, expected in [([0, 0, 1, 1], True), ([0, 0, .5, .5], False)]:
            score = evidence_support(q, [], DOCUMENTS, [{'document': 'sae', 'page': 24, 'region': region}])
            self.assertEqual(score['complete_support'], expected)
        self.assertFalse(evidence_support(q, [], DOCUMENTS, [{'document': 'gr25', 'page': 24, 'region': [0, 0, 1, 1]}])['complete_support'])

    def test_image_budget_does_not_credit_unattached_fourth_page(self):
        q = question(groups=[group([figure()])], hits=[hit(page=page) for page in (1, 2, 3, 24)], requires_image=True)
        score_result(q, DOCUMENTS)
        self.assertFalse(q['planned_context_support']['complete_support'])
        self.assertEqual(len(q['planned_answer_context']['images']), 3)

    def test_citation_must_be_in_actual_context_not_retrieval_pool(self):
        q = question(hits=[hit()] + [hit('Other', page=25)])
        q['answer_context'] = answer_context(q, DOCUMENTS, max_chunks=1)
        answer = {'answer': '350 mm', 'citations': [{'document': 'sae', 'page': 25}], 'abstained': False}
        self.assertFalse(eval_module.check_answer(q, answer, DOCUMENTS)['citation_provenance_pass'])

    def test_correct_answer_without_support_fails_groundedness_proxy(self):
        q = question(hits=[hit('Unrelated', start=20)])
        q['answer_context'] = answer_context(q, DOCUMENTS)
        answer = {'answer': '350 mm', 'citations': [{'document': 'sae', 'page': 24}], 'abstained': False}
        checks = eval_module.check_answer(q, answer, DOCUMENTS)
        self.assertTrue(checks['expected_behavior_pass'])
        self.assertTrue(checks['citation_provenance_pass'])
        self.assertFalse(checks['grounded_answer_proxy_pass'])

    def test_every_required_evidence_group_must_be_cited(self):
        q = question(groups=[group([span(page=24), span('upload', page=25)])], hits=[hit(), hit('upload', page=25)])
        q['answer_context'] = answer_context(q, DOCUMENTS)
        answer = {'answer': '350 mm', 'citations': [{'document': 'sae-id', 'page': 24}], 'abstained': False}
        self.assertFalse(eval_module.check_answer(q, answer, DOCUMENTS)['cited_evidence_complete_pass'])
        answer['citations'].append({'document': 'sae', 'page': 25})
        self.assertTrue(eval_module.check_answer(q, answer, DOCUMENTS)['grounded_answer_proxy_pass'])

    def test_unanswerable_requires_abstention_and_no_fabricated_citations(self):
        q = question(groups=[], expect_abstention=True)
        q['answer_context'] = answer_context(q, DOCUMENTS)
        answer = {'answer': 'Not established', 'citations': [], 'abstained': True}
        self.assertTrue(eval_module.check_answer(q, answer, DOCUMENTS)['grounded_answer_proxy_pass'])
        answer['abstained'] = False
        self.assertFalse(eval_module.check_answer(q, answer, DOCUMENTS)['grounded_answer_proxy_pass'])

    def test_page_hit_requires_matching_document(self):
        q = question(hits=[hit(document='gr25'), hit()])
        eval_module.score_pages(q, DOCUMENTS)
        self.assertFalse(q['hit_at_1'])
        self.assertTrue(q['hit_at_5'])
        self.assertEqual(q['reciprocal_rank'], .5)

    def test_fixture_requires_valid_quote_offsets_and_groups(self):
        suite = {'version': 2, 'documents': [{'key': 'sae'}], 'questions': [question()]}
        validate_suite(suite, {'sae': [''] * 23 + ['350 mm']})
        damaged = copy.deepcopy(suite)
        damaged['questions'][0]['evidence_groups'][0]['alternatives'][0]['spans'][0]['quote'] = '600 mm'
        with self.assertRaisesRegex(ValueError, 'mismatch'):
            validate_suite(damaged, {'sae': [''] * 23 + ['350 mm']})
        suite['questions'][0]['evidence_groups'] = []
        with self.assertRaisesRegex(ValueError, 'exactly when answerable'):
            validate_suite(suite)

    def test_summary_excludes_abstentions_from_evidence_recall(self):
        positive = question(mode='lexical', latency_ms=2)
        negative = question(groups=[], expect_abstention=True, mode='lexical', latency_ms=3)
        for q in [positive, negative]:
            score_result(q, DOCUMENTS)
            eval_module.score_pages(q, DOCUMENTS)
        summary = eval_module.summarize({'results': [positive, negative]})['lexical']
        self.assertEqual(summary['evidence_at_k']['5']['questions'], 1)
        self.assertEqual(summary['evidence_at_k']['5']['evidence_recall'], 1)
        summary = eval_module.summarize({'results': [negative]})['lexical']
        self.assertIsNone(summary['evidence_at_k']['5']['evidence_recall'])

    def test_failed_answer_calls_are_not_omitted_from_summary(self):
        q = question(mode='hybrid', latency_ms=2, answer_latency_ms=30, answer_error='Provider unavailable')
        score_result(q, DOCUMENTS)
        eval_module.score_pages(q, DOCUMENTS)
        summary = eval_module.summarize({'results': [q]})['hybrid']
        self.assertEqual(summary['answer_error_count'], 1)
        self.assertEqual(summary['grounded_answer_proxy_pass_rate'], 0)


if __name__ == '__main__':
    unittest.main()
