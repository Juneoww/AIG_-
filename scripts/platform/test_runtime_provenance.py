"""验证固定运行时来源、缓存内容和规则过滤边界。"""
import importlib.util
import io
import json
from pathlib import Path
import tarfile
import tempfile
import unittest

spec=importlib.util.spec_from_file_location('runtime_builder',Path(__file__).parents[1]/'build-platform-images.py')
builder=importlib.util.module_from_spec(spec)
spec.loader.exec_module(builder)


class RuntimeProvenanceTest(unittest.TestCase):
    def test_mutable_tag_cannot_substitute_unknown_runtime(self):
        with self.assertRaises(ValueError):
            builder.require_source({'Id':'sha256:'+'0'*64,'Architecture':'amd64','Os':'linux'},'agent')

    def test_changed_or_wrong_source_cache_refused(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);cache=root/'rootfs.tar';record=root/'origin.json'
            cache.write_bytes(b'original')
            record.write_text(json.dumps({'image_id':builder.SOURCE_IDS['agent'],'sha256':builder.file_hash(cache),'source_sha256':'source'}))
            builder.verify_cache(cache,'agent',record,'source')
            with self.assertRaises(ValueError):builder.verify_cache(cache,'agent',record,'wrong-source')
            cache.write_bytes(b'changed')
            with self.assertRaises(ValueError):builder.verify_cache(cache,'agent',record,'source')

    def test_clean_layer_keeps_other_data_and_excludes_every_rule(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);source=root/'source.tar';clean=root/'clean.tar'
            with tarfile.open(source,'w') as archive:
                for name in ('app/data/fingerprints/a.yaml','app/data/vuln/a.yaml','app/data/vuln_en/a.yaml','app/data/mcp/keep.json','app/agent'):
                    member=tarfile.TarInfo(name);member.size=4;archive.addfile(member,io.BytesIO(b'test'))
            builder.clean_rootfs(source,clean)
            with tarfile.open(clean) as archive:self.assertEqual(archive.getnames(),['app/data/mcp/keep.json'])


if __name__=='__main__':unittest.main()
