"""Retain partial native bytes after a failed build without candidate authority."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess


def retain(source, output, controller):
    if not re.fullmatch("[a-f0-9]{40}",controller):
        raise ValueError("diagnostics require an immutable controller commit")
    if output.exists() or output.is_symlink() or output.resolve().is_relative_to(source.resolve()):
        raise ValueError("diagnostics require a fresh output outside source")
    source=source.resolve(strict=True)
    def git(value):
        return subprocess.check_output(["git","--no-replace-objects","-C",str(source),"rev-parse",value],text=True).strip()
    commit,tree=git("HEAD"),git("HEAD^{tree}")
    bundle=source/"lycaon-den/src-tauri/target"
    selected=sorted(path for path in bundle.glob("*/release/bundle/*/*")
                    if path.suffix in {".deb",".rpm"} and path.is_file())
    if not selected or len(selected)>16:
        raise ValueError("partial diagnostics require a bounded nonempty native package set")
    output.mkdir(parents=True,mode=0o700)
    try:
        rows=[]
        for index,path in enumerate(selected):
            if (not path.resolve(strict=True).is_relative_to(source.resolve(strict=True))
                    or any(parent.is_symlink() for parent in [path,*path.parents] if parent!=source.parent)):
                raise ValueError("partial package escaped its physical source output tree")
            fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW)
            target=output/(str(index)+"-"+path.name)
            digest=hashlib.sha256()
            with os.fdopen(fd,"rb") as incoming,target.open("xb") as retained:
                before=os.fstat(incoming.fileno())
                if not stat.S_ISREG(before.st_mode) or not 0<before.st_size<=16<<30:
                    raise ValueError("partial package is not a bounded regular file")
                while block:=incoming.read(1<<20):
                    retained.write(block)
                    digest.update(block)
                after=os.fstat(incoming.fileno())
                identity=lambda row:(row.st_dev,row.st_ino,row.st_size,row.st_mtime_ns,row.st_ctime_ns)
                if identity(before)!=identity(after) or identity(after)!=identity(path.lstat()):
                    raise ValueError("partial package changed during retention")
            target.chmod(0o600)
            rows.append({"file":target.name,"source_path":path.relative_to(source).as_posix(),
                         "sha256":digest.hexdigest(),"bytes":before.st_size})
        report={"format_version":1,"scope":"partial-build-diagnostics","source_commit":commit,"source_tree":tree,
                "controller_commit":controller,"complete_candidate":False,"installed_qualified":False,
                "publication_authority":False,"packages":rows}
        (output/"diagnostics.json").write_text(json.dumps(report,sort_keys=True,indent=2)+"\n")
        return report
    except BaseException:
        shutil.rmtree(output)
        raise


def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source",type=Path,required=True)
    parser.add_argument("--output",type=Path,required=True)
    parser.add_argument("--controller",required=True)
    args=parser.parse_args()
    retain(args.source,args.output,args.controller)


if __name__=="__main__":
    main()
