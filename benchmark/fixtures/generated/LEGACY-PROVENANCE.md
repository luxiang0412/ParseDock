# Legacy OLE2 fixtures

Both files come from the Apache POI project's test-data directory (Apache-2.0):

| File | Source | Licence |
| --- | --- | --- |
| `legacy_word6.doc` | `apache/poi` `test-data/document/Word6.doc` | Apache-2.0 |
| `legacy_simple.xls` | `apache/poi` `test-data/spreadsheet/Simple.xls` | Apache-2.0 |

They exist to prove that legacy Word/Excel (OLE2 compound files) are accepted and
parsed, and that the parsers degrade honestly (text + tables, no page geometry).
`.doc`/`.xls` are out of scope for the synthetic generator; see
`benchmark/scripts/gen_fixtures.py` for everything else.
