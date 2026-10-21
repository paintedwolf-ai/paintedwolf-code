"""Assemble signed Linux qualification controls without publishing any repository.

The command line is the disposable-identity rehearsal; protected production assembly
runs through control_pipeline, which supplies its current controller identity."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil

from ..installed_provenance import controller_context
from ..openpgp import selection
from .authority import repository_origin
from .control_packages import FAMILIES, ARCHITECTURES, stage, assemble
from .control_cache import build as caches
from .generations import CATALOG
from ..release_source import facts
from ..release_admission import document


def build(native_manifest, inputs, output, source, catalog, private, base_url, previous, nix_inputs,
          *, producer, source_repository, rehearsal=False, families=tuple(sorted(FAMILIES)), architectures=tuple(ARCHITECTURES)):
    if not rehearsal and (set(families) != FAMILIES or set(architectures) != set(ARCHITECTURES)):
        raise ValueError("production controls require every supported native package family and CPU")
    if not isinstance(previous,dict):
        raise ValueError("repository predecessor selections must be an object")
    if not rehearsal and catalog.resolve() != CATALOG.resolve():
        raise ValueError("production controls require the protected controller signing catalog")
    if output.exists() or output.is_symlink() or output.resolve().is_relative_to(inputs.resolve(strict=True)):
        raise ValueError("qualification controls require a fresh independent output directory")
    signer,_ = selection(catalog,rehearsal)
    if rehearsal:
        proof={"format_version":1,"rehearsal":True,"source_commit":source["source_commit"],"source_tree":source["source_tree"]}
    else:
        if producer != controller_context(os.environ):
            raise ValueError("production controls require their current protected controller identity")
        proof={"format_version":1,**producer,"source_commit":source["source_commit"],"source_tree":source["source_tree"]}
    output.mkdir(parents=True,mode=0o700)
    try:
        for name in ("generations","nix-cache","repository-setup"):
            (output/name).mkdir(mode=0o700)
        selected=stage(native_manifest,inputs,output/"packages",source,families,architectures)
        repositories=assemble(selected,output,source,catalog,private,base_url,previous,rehearsal=rehearsal)
        nix_artifacts,nix_reports=caches(nix_inputs,output/"nix-cache",source,rehearsal=rehearsal,architectures=architectures,source_repository=source_repository)
        final={"format_version":1,"source_commit":source["source_commit"],"artifacts":[*selected.values(),*nix_artifacts]}
        for name,value in (("final-artifacts.json",final),("repositories.json",repositories),("control-producer.json",proof)):
            with (output/name).open("x") as stream:
                json.dump(value,stream,sort_keys=True,indent=2)
                stream.write("\n")
        return {"format_version":1,"rehearsal":rehearsal,"published":False,"application_qualified":False,
                "source":source,"signing_generation":signer["generation"],"key_fingerprint":signer["fingerprint"],
                "artifact_count":len(final["artifacts"]),"generations":[entry["operation"]["generation"] for entry in repositories["generations"]],
                "nix_caches":nix_reports}
    except BaseException:
        shutil.rmtree(output)
        raise


def main(arguments=None):
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--native-artifacts",type=Path,required=True)
    parser.add_argument("--inputs",type=Path,required=True)
    parser.add_argument("--source-repository",type=Path,required=True)
    parser.add_argument("--source-commit",required=True)
    parser.add_argument("--signing-catalog",type=Path,default=CATALOG)
    parser.add_argument("--private-key",type=Path,required=True)
    parser.add_argument("--repository-base-url",required=True)
    parser.add_argument("--predecessors",type=Path,required=True)
    parser.add_argument("--nix-inputs",type=Path,required=True)
    parser.add_argument("--output",type=Path,required=True)
    parser.add_argument("--receipt",type=Path,required=True)
    parser.add_argument("--family",action="append",choices=sorted(FAMILIES))
    parser.add_argument("--architecture",action="append",choices=sorted(ARCHITECTURES))
    args=parser.parse_args(arguments)
    try:
        source=facts(args.source_repository,args.source_commit)
        if (args.output.resolve().is_relative_to(args.source_repository.resolve()) or args.receipt.exists()
                or args.receipt.is_symlink() or args.receipt.resolve().is_relative_to(args.output.resolve())
                or args.receipt.resolve().is_relative_to(args.source_repository.resolve())):
            raise ValueError("control output and receipt must not replace source or retained inputs")
        # Validate the origin separately from its explicit immutable repository prefix.
        from urllib.parse import urlsplit
        url=urlsplit(args.repository_base_url)
        repository_origin(url.scheme+"://"+url.netloc)
        if url.query or url.fragment or any(character.isspace() for character in args.repository_base_url):
            raise ValueError("repository base URL has invalid publication framing")
        native,_=document(args.native_artifacts)
        previous,_=document(args.predecessors)
        nix,_=document(args.nix_inputs)
        if not isinstance(nix,dict) or set(nix)!={"format_version","systems"} or type(nix["format_version"]) is not int or nix["format_version"]!=1:
            raise ValueError("Nix control inputs require format_version1 and explicit systems")
        report=build(native,args.inputs,args.output,source,args.signing_catalog,args.private_key,args.repository_base_url,previous,nix["systems"],
                     producer=None,source_repository=args.source_repository,rehearsal=True,
                     families=tuple(args.family or sorted(FAMILIES)),architectures=tuple(args.architecture or ARCHITECTURES))
        args.receipt.parent.mkdir(parents=True,exist_ok=True)
        with args.receipt.open("x") as stream:
            json.dump(report,stream,sort_keys=True,indent=2)
            stream.write("\n")
        print(f"Nonpublishing Linux controls assembled: {args.output}")
    except (OSError,ValueError,TypeError,KeyError,ImportError) as error:
        parser.exit(1,"Linux control assembly refused: "+str(error)+"\n")
    return 0


if __name__=="__main__":
    raise SystemExit(main())
