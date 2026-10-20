"""Select exact native candidate bytes for disposable installed interaction campaigns."""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import re
import shutil
import tempfile

from .installed_artifact_bundle import download
from .installed_provenance import artifact_for_run
from .installed_runtime import file_identity
from .release_source import facts

ARCHES = {"amd64":"x86_64", "arm64":"aarch64"}
EXTENSIONS = {"deb":"deb", "rpm":"rpm", "pacman":"pkg.tar.zst"}


def select(api, repository, repository_id, run_id, artifact_id, controller, architecture, partial):
    if (architecture not in ARCHES or type(repository_id) is not int or repository_id<1
            or type(run_id) is not int or run_id<1 or type(artifact_id) is not int or artifact_id<1
            or not isinstance(controller,str) or not re.fullmatch('[a-f0-9]{40}',controller)
            or not isinstance(repository,str) or not re.fullmatch(r'[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+',repository)
            or type(partial) is not bool):
        raise ValueError('candidate selection requires exact typed producer and native CPU identities')
    run=api(f'repos/{repository}/actions/runs/{run_id}')
    if (type(run.get('id')) is not int or run['id']!=run_id or run.get('head_sha')!=controller
            or run.get('repository',{}).get('id')!=repository_id
            or run.get('repository',{}).get('full_name')!=repository
            or run.get('head_repository',{}).get('id')!=repository_id
            or run.get('path')!='.github/workflows/linux-candidates.yml'
            or run.get('head_branch') not in {'main','linux/candidate-ci-isolation'}
            or run.get('event') not in {'push','workflow_dispatch','schedule'}
            or run.get('status')!='completed' or run.get('conclusion')!=('failure' if partial else 'success')
            or type(run.get('run_attempt')) is not int or run['run_attempt']<1):
        raise ValueError('candidate input run differs from its independently pinned native producer')
    role=('linux-partial-diagnostics-' if partial else 'linux-candidate-')+'linux-'+ARCHES[architecture]
    artifact=artifact_for_run(api,repository,repository_id,run_id,controller,role)
    if artifact['artifact_id']!=artifact_id:
        raise ValueError('candidate artifact ID differs from its independently selected role')
    return {'repository':repository,'repository_id':repository_id,'run_id':run_id,
            'run_attempt':run['run_attempt'],'controller_commit':controller,**artifact}


def read(path):
    if path.is_symlink() or not path.is_file() or path.stat().st_size>1<<20:
        raise ValueError('candidate metadata requires bounded regular retained bytes')
    return json.loads(path.read_text())


def package_rows(root, source, controller, architecture, partial):
    native=ARCHES[architecture]
    if partial:
        report=read(root/'diagnostics.json')
        expected={'format_version','scope','source_commit','source_tree','controller_commit',
                  'complete_candidate','installed_qualified','publication_authority','packages'}
        if (not isinstance(report,dict) or set(report)!=expected or type(report['format_version']) is not int
                or report['format_version']!=1 or report['scope']!='partial-build-diagnostics'
                or report['source_commit']!=source['source_commit'] or report['source_tree']!=source['source_tree']
                or report['controller_commit']!=controller
                or any(report[key] is not False for key in ('complete_candidate','installed_qualified','publication_authority'))):
            raise ValueError('partial package diagnostics differ from their exact producer and source')
        if not isinstance(report['packages'],list) or len(report['packages'])>16:
            raise ValueError('partial package diagnostics exceed their fixed producer inventory')
        rows=[]
        for row in report['packages']:
            if not isinstance(row,dict) or set(row)!={'file','source_path','sha256','bytes'}:
                raise ValueError('partial package diagnostic row has an unknown shape')
            original=Path(row['source_path'])
            family=original.parent.name
            # Older diagnostic producers also retained the raw Tauri DEB. Only the
            # canonical package assembler output can become an installed fixture.
            if family not in {'deb','rpm'} or not original.name.startswith('painted-wolf-code'):
                continue
            triple={'amd64':'x86_64','arm64':'aarch64'}[architecture]+'-unknown-linux-gnu'
            if original.parts[:-1]!=('lycaon-den','src-tauri','target',triple,'release','bundle',family):
                raise ValueError('partial package path differs from the selected native CPU output')
            rows.append({**row,'family':family})
    else:
        for name,value in [('source-commit.txt',source['source_commit']),('source-tree.txt',source['source_tree']),
                           ('controller-commit.txt',controller)]:
            path=root/name
            if path.is_symlink() or not path.is_file() or path.stat().st_size>128 or path.read_text().strip()!=value:
                raise ValueError('native candidate metadata differs from its exact producer and source')
        report=read(root/'candidate-packages.json')
        expected={'format_version','source_commit','version','platform','architecture','packages','qualification_status','published'}
        if (not isinstance(report,dict) or set(report)!=expected or type(report['format_version']) is not int
                or report['format_version']!=1 or report['source_commit']!=source['source_commit']
                or report['version']!=source['version'] or report['architecture']!=native
                or report['platform']!='linux-'+native or report['qualification_status']!='unverified'
                or report['published'] is not False):
            raise ValueError('native candidate inventory differs from its exact source and native CPU')
        rows=report['packages']
        if not isinstance(rows,list):
            raise ValueError('native candidate packages must be a list')
        if any(not isinstance(row,dict) or set(row)!={'family','file','architecture','sha256','bytes'}
               or row['architecture']!=native for row in rows):
            raise ValueError('native candidate package rows differ from their selected CPU')
    if not isinstance(rows,list) or not rows or len(rows)>3:
        raise ValueError('candidate inputs require a bounded nonempty native package selection')
    families=[row['family'] for row in rows]
    if (len(set(families))!=len(families) or not set(families)<=set(EXTENSIONS)
            or not partial and set(families)!=set(EXTENSIONS)):
        raise ValueError('candidate input package families are incomplete or repeated')
    return rows


def prepare(api, repository, repository_id, run_id, artifact_id, controller, source_repository,
            source_commit, source_tree, architecture, output, *, partial=False):
    selected=select(api,repository,repository_id,run_id,artifact_id,controller,architecture,partial)
    source=facts(source_repository,source_commit)
    if source['source_tree']!=source_tree:
        raise ValueError('candidate source tree differs from its immutable Git object')
    if output.exists() or output.is_symlink() or output.resolve().is_relative_to(source_repository.resolve()):
        raise ValueError('candidate installed inputs require a fresh independent output')
    with tempfile.TemporaryDirectory(prefix='paintedwolf-native-candidate-',dir=output.parent) as temporary:
        retained=Path(temporary)/'selected'
        download(selected,retained)
        rows=package_rows(retained,source,controller,architecture,partial)
        output.mkdir(mode=0o700)
        try:
            packages=output/'packages'
            packages.mkdir(mode=0o700)
            artifacts=[]
            for row in rows:
                name=row['file']
                if not isinstance(name,str) or Path(name).name!=name or name in {'.','..'}:
                    raise ValueError('candidate package path is not a retained root file')
                actual=file_identity(retained/name)
                if (not isinstance(row['sha256'],str) or not re.fullmatch('[a-f0-9]{64}',row['sha256'])
                        or type(row['bytes']) is not int or row['bytes']<1
                        or actual['sha256']!=row['sha256'] or actual['bytes']!=row['bytes']):
                    raise ValueError('candidate package differs from its selected final bytes')
                artifact_id=f"native-{row['family']}-{architecture}.{EXTENSIONS[row['family']]}"
                target=packages/artifact_id
                shutil.copyfile(retained/name,target)
                target.chmod(0o600)
                copied=file_identity(target)
                if copied['sha256']!=actual['sha256'] or copied['bytes']!=actual['bytes']:
                    raise ValueError('candidate package changed during installed fixture retention')
                artifacts.append({'id':artifact_id,'installation':row['family'],'architecture':architecture,
                                  'source_commit':source_commit,'identity':{'kind':'file','sha256':actual['sha256']}})
            final={'format_version':1,'source_commit':source_commit,'artifacts':artifacts}
            report={'format_version':1,'source':source,'provenance':selected,'partial':partial,
                    'qualification_authority':False,'publication_authority':False}
            for name,value in [('final-artifacts.json',final),('candidate-selection.json',report)]:
                (output/name).write_text(json.dumps(value,sort_keys=True,indent=2)+'\n')
            return report
        except BaseException:
            shutil.rmtree(output)
            raise


def main(arguments=None):
    parser=argparse.ArgumentParser(description=__doc__)
    for name in ('repository','controller-commit','source-commit','source-tree'):
        parser.add_argument('--'+name,required=True)
    for name in ('repository-id','run-id','artifact-id'):
        parser.add_argument('--'+name,type=int,required=True)
    parser.add_argument('--architecture',choices=tuple(ARCHES),required=True)
    parser.add_argument('--source-repository',type=Path,required=True)
    parser.add_argument('--output',type=Path,required=True)
    parser.add_argument('--partial',action='store_true')
    args=parser.parse_args(arguments)
    try:
        from scripts.ci_policy.github import api
        prepare(api,args.repository,args.repository_id,args.run_id,args.artifact_id,args.controller_commit,
                args.source_repository,args.source_commit,args.source_tree,args.architecture,args.output,partial=args.partial)
    except (OSError,ValueError,TypeError,KeyError) as error:
        parser.exit(1,'Native installed fixture selection refused: '+str(error)+'\n')
    return 0


if __name__=='__main__':
    raise SystemExit(main())
