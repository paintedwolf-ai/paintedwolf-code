package apitest

import "github.com/lycaon/lycaon/pkg/api"

func NewPromptReferencePathFile(part api.PromptReferencePathFilePart) api.PromptReferencePart {
	part.Kind = api.PromptReferencePathFile
	return api.PromptReferencePart{PathFile: &part}
}

func NewPromptReferencePathFolder(part api.PromptReferencePathFolderPart) api.PromptReferencePart {
	part.Kind = api.PromptReferencePathFolder
	return api.PromptReferencePart{PathFolder: &part}
}

func NewPromptReferenceExternalFile(part api.PromptReferenceExternalFilePart) api.PromptReferencePart {
	part.Kind = api.PromptReferenceExternalFile
	return api.PromptReferencePart{ExternalFile: &part}
}

func NewPromptReferenceExternalFolder(part api.PromptReferenceExternalFolderPart) api.PromptReferencePart {
	part.Kind = api.PromptReferenceExternalFolder
	return api.PromptReferencePart{ExternalFolder: &part}
}

func NewPromptReferenceArtifact(part api.PromptReferenceArtifactPart) api.PromptReferencePart {
	part.Kind = api.PromptReferenceArtifact
	return api.PromptReferencePart{Artifact: &part}
}

func NewPromptReferenceSearchHit(part api.PromptReferenceSearchHitPart) api.PromptReferencePart {
	part.Kind = api.PromptReferenceSearchHit
	return api.PromptReferencePart{SearchHit: &part}
}
