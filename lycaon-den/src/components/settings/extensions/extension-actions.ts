import { createSignal, type Accessor } from "solid-js";
import type { LycaonClient } from "../../../api/client.ts";
import type { ExtensionsWriteTarget } from "../../../api/http-capabilities/extensions.ts";
import type {
  ExtensionPackSummary,
  ExtensionPackUpdateStatus,
  ExtensionMetaPackSummary,
  ExtensionsCatalogView,
} from "../../../api/types.ts";
import { noticeFromCaught, noticeFromInput, type AppNotice } from "../../../notices/notice-model.ts";
import { APP_SCOPE } from "../../../notices/notice-scope.ts";
import { EXTENSIONS_SETTINGS_COPY as C } from "../../../settings/extensions/extensions-settings-copy.ts";
import { pickProjectFolder } from "../../../platform/files/folder.ts";
import { confirmDestructive } from "../../../platform/interaction/confirm-dialog.ts";

type ExtensionActionsOptions = {
  client: Accessor<LycaonClient>;
  captureCatalog: () => { publish: (view: ExtensionsCatalogView) => unknown };
  stateRevision: Accessor<string>;
  selectedPackId: Accessor<string | undefined>;
  setSelectedPackId: (value: string | undefined) => unknown;
  setDetailRevision: (update: (value: number) => number) => unknown;
  profilePacks: Accessor<ExtensionPackSummary[]>;
  isProject: Accessor<boolean>;
  unitWriteTarget: Accessor<ExtensionsWriteTarget>;
};

export function createExtensionActions(options: ExtensionActionsOptions) {
  const { client, captureCatalog, stateRevision, selectedPackId, setSelectedPackId,
    setDetailRevision, profilePacks, isProject, unitWriteTarget } = options;
  const [actionError, setActionError] = createSignal<AppNotice | undefined>();
  const [actionStatus, setActionStatus] = createSignal<string | undefined>();
  const [busy, setBusy] = createSignal(false);
  const [addOpen, setAddOpen] = createSignal(false);
  const [addUrl, setAddUrl] = createSignal("");
  const [addRef, setAddRef] = createSignal("");
  const [addVersion, setAddVersion] = createSignal("");
  const [addError, setAddError] = createSignal<AppNotice | undefined>();
  const [updatePlan, setUpdatePlan] = createSignal<
    Record<string, ExtensionPackUpdateStatus | undefined>
  >({});
  const [suiteInstallOpen, setSuiteInstallOpen] = createSignal(false);
  const [suiteInstallUrl, setSuiteInstallUrl] = createSignal("");
  const [suiteInstallRef, setSuiteInstallRef] = createSignal("");
  const [suiteInstallVersion, setSuiteInstallVersion] = createSignal("");
  const [suiteInstallError, setSuiteInstallError] = createSignal<AppNotice | undefined>();
  const [profileOpen, setProfileOpen] = createSignal(false);
  const [profilePackId, setProfilePackId] = createSignal("");
  const [profileName, setProfileName] = createSignal("");
  const [profileError, setProfileError] = createSignal<AppNotice | undefined>();
  const catchNotice = (err: unknown, fallback: string) =>
    noticeFromCaught(err, APP_SCOPE, { title: fallback, message: fallback });
  const copyNotice = (message: string) =>
    noticeFromInput({ title: message, message }, APP_SCOPE);
  // Committed writes invalidate older reloads.
  const runAction = async (
    fn: () => Promise<{ view: ExtensionsCatalogView } | void>,
  ) => {
    setBusy(true);
    setActionError(undefined);
    setActionStatus(undefined);
    try {
      const target = captureCatalog();
      const res = await fn();
      if (res) {
        target.publish(res.view);
      }
      setDetailRevision((n) => n + 1);
    } catch (err) {
      setActionError(catchNotice(err, C.actionError));
    } finally {
      setBusy(false);
    }
  };

  const warnDisablePack = async (pack: ExtensionPackSummary): Promise<boolean> => {
    const f = (pack.feature ?? "").toLowerCase();
    if (f === "platform") {
      return confirmDestructive({
        title: "Disable platform?",
        message: C.platformDisableWarn,
        okLabel: "Disable platform",
      });
    }
    if (f === "security") {
      return confirmDestructive({
        title: "Disable security?",
        message: C.securityDisableWarn,
        okLabel: "Disable security",
      });
    }
    return true;
  };

  const togglePack = (
    pack: ExtensionPackSummary,
    enabled: boolean,
    input?: HTMLInputElement,
  ) => {
    void (async () => {
      if (!enabled && !(await warnDisablePack(pack))) {
        if (input) input.checked = pack.enabled;
        return;
      }
      await runAction(async () => {
        const updateExtensionPackRes = await client().updateExtensionPack(
          pack.id,
          { enabled, expected_revision: stateRevision() },
        );
        setActionStatus(C.deviceBanner);
        return updateExtensionPackRes;
      });
    })();
  };

  const removePack = (pack: ExtensionPackSummary) => {
    if (!pack.removable) return;
    void runAction(async () => {
      const removeExtensionPackRes = await client().deleteExtensionPack(
        pack.id,
        stateRevision(),
      );
      if (selectedPackId() === pack.id) setSelectedPackId(undefined);
      return removeExtensionPackRes;
    });
  };

  const beginUpdate = (pack: ExtensionPackSummary) => {
    void runAction(async () => {
      const res = await client().getExtensionPackUpdate(pack.id);
      setUpdatePlan((prev) => ({ ...prev, [pack.id]: res }));
      setActionStatus(
        res.message || (res.available ? "Update available" : C.upToDate),
      );
    });
  };

  const confirmUpdate = (pack: ExtensionPackSummary) => {
    void runAction(async () => {
      const updateExtensionPackRes = await client().updateExtensionPack(
        pack.id,
        { expected_revision: stateRevision() },
      );
      setUpdatePlan((prev) => ({ ...prev, [pack.id]: undefined }));
      setActionStatus(C.updatePack);
      return updateExtensionPackRes;
    });
  };

  const reloadPack = (pack: ExtensionPackSummary) => {
    void runAction(async () => {
      const reloadExtensionPackRes = await client().reloadExtensionPack(
        pack.id,
        { expected_revision: stateRevision() },
      );
      setActionStatus(C.reloadFromDisk);
      return reloadExtensionPackRes;
    });
  };

  const installFromGit = () => {
    const url = addUrl().trim();
    if (!url) {
      setAddError(copyNotice(C.addUrlRequired));
      return;
    }
    if (addVersion().trim() && addRef().trim()) {
      setAddError(copyNotice(C.versionOrRef));
      return;
    }
    setAddError(undefined);
    void runAction(async () => {
      const installExtensionPackRes = await client().installExtensionPack(
        {
          source: url,
          version: addVersion().trim() || undefined,
          ref: addRef().trim() || undefined,
          expected_revision: stateRevision(),
        },
      );
      setAddOpen(false);
      setAddUrl("");
      setAddVersion("");
      setAddRef("");
      setActionStatus(C.deviceBanner);
      return installExtensionPackRes;
    });
  };

  const installFromFolder = () => {
    void runAction(async () => {
      const folder = await pickProjectFolder();
      if (!folder) return;
      const source = folder.startsWith("path:") || folder.startsWith("file:")
        ? folder
        : `path:${folder}`;
      const installExtensionPackRes = await client().installExtensionPack(
        { source, expected_revision: stateRevision() },
      );
      setActionStatus(C.deviceBanner);
      return installExtensionPackRes;
    });
  };

  const openSuiteInstallDialog = () => {
    setSuiteInstallUrl("");
    setSuiteInstallVersion("");
    setSuiteInstallRef("");
    setSuiteInstallError(undefined);
    setSuiteInstallOpen(true);
  };

  const installSuite = () => {
    const url = suiteInstallUrl().trim();
    if (!url) {
      setSuiteInstallError(copyNotice(C.suiteInstallUrlRequired));
      return;
    }
    if (suiteInstallVersion().trim() && suiteInstallRef().trim()) {
      setSuiteInstallError(copyNotice(C.versionOrRef));
      return;
    }
    setSuiteInstallError(undefined);
    void runAction(async () => {
      const installExtensionMetaPackRes = await client().installExtensionMetaPack(
        {
          source: url,
          version: suiteInstallVersion().trim() || undefined,
          ref: suiteInstallRef().trim() || undefined,
          expected_revision: stateRevision(),
        },
      );
      setSuiteInstallOpen(false);
      setSuiteInstallUrl("");
      setSuiteInstallVersion("");
      setSuiteInstallRef("");
      setActionStatus(C.deviceBanner);
      return installExtensionMetaPackRes;
    });
  };

  const enableSuite = (metaId: string) => {
    void runAction(async () => {
      const applyExtensionMetaPackRes = await client().applyExtensionMetaPack(
        metaId,
        { expected_revision: stateRevision() },
      );
      setActionStatus(C.deviceBanner);
      return applyExtensionMetaPackRes;
    });
  };

  const disableSuite = (metaId: string) => {
    void runAction(async () => {
      const response = await client().updateExtensionMetaPack(metaId, {
        enabled: false,
        expected_revision: stateRevision(),
      });
      setActionStatus(C.deviceBanner);
      return response;
    });
  };

  const removeSuite = (meta: ExtensionMetaPackSummary) => {
    if (!meta.removable) return;
    void (async () => {
      const confirmed = await confirmDestructive({
        title: "Remove this suite?",
        message: C.suiteRemoveConfirm,
        okLabel: "Remove suite",
      });
      if (!confirmed) return;
      await runAction(async () => {
        const removeExtensionMetaPackRes = await client().deleteExtensionMetaPack(
          meta.id,
          stateRevision(),
        );
        return removeExtensionMetaPackRes;
      });
    })();
  };

  const applyProfile = () => {
    const packId = profilePackId().trim();
    const name = profileName().trim();
    if (!packId) {
      setProfileError(copyNotice(C.profilePackRequired));
      return;
    }
    if (!name) {
      setProfileError(copyNotice(C.profileNameRequired));
      return;
    }
    setProfileError(undefined);
    void runAction(async () => {
      const applyExtensionPackProfileRes = await client().applyExtensionPackProfile(
        packId,
        name,
        { expected_revision: stateRevision() },
      );
      setProfileOpen(false);
      setActionStatus(C.deviceBanner);
      return applyExtensionPackProfileRes;
    });
  };

  const openAddDialog = () => {
    setAddError(undefined);
    setAddUrl("");
    setAddVersion("");
    setAddRef("");
    setAddOpen(true);
  };

  const openProfileDialog = () => {
    setProfileError(undefined);
    const first = profilePacks()[0];
    setProfilePackId(first?.id ?? "");
    setProfileName("");
    setProfileOpen(true);
  };

  const setUnitDisabled = (unitId: string, disabled: boolean) => {
    void runAction(async () => {
      const updateExtensionUnitRes = await client().updateExtensionUnit(
        unitId,
        { enabled: !disabled, expected_revision: stateRevision() },
        unitWriteTarget(),
      );
      if (!isProject()) setActionStatus(C.deviceBanner);
      return updateExtensionUnitRes;
    });
  };

  const setUnitOwn = (unitId: string, packId: string | null) => {
    void runAction(async () => {
      const result = await client().updateExtensionUnit(
        unitId,
        { own_pack_id: packId, expected_revision: stateRevision() },
      );
      setActionStatus(C.deviceBanner);
      return result;
    });
  };

  return {
    actionError,
    setActionError,
    actionStatus,
    setActionStatus,
    busy,
    addOpen,
    setAddOpen,
    addUrl,
    setAddUrl,
    addRef,
    setAddRef,
    addVersion,
    setAddVersion,
    addError,
    updatePlan,
    suiteInstallOpen,
    setSuiteInstallOpen,
    suiteInstallUrl,
    setSuiteInstallUrl,
    suiteInstallRef,
    setSuiteInstallRef,
    suiteInstallVersion,
    setSuiteInstallVersion,
    suiteInstallError,
    profileOpen,
    setProfileOpen,
    profilePackId,
    setProfilePackId,
    profileName,
    setProfileName,
    profileError,
    catchNotice,
    copyNotice,
    togglePack,
    removePack,
    beginUpdate,
    confirmUpdate,
    reloadPack,
    installFromGit,
    installFromFolder,
    openSuiteInstallDialog,
    installSuite,
    enableSuite,
    disableSuite,
    removeSuite,
    applyProfile,
    openAddDialog,
    openProfileDialog,
    setUnitDisabled,
    setUnitOwn,
  };
}
