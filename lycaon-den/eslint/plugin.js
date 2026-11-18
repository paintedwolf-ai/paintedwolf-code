import noComputationInJsxCallback from "./rules/no-computation-in-jsx-callback.js";
import noHttpErrorStatusBranching from "./rules/no-http-error-status-branching.js";
import noKeyedSettingsDraft from "./rules/no-keyed-settings-draft.js";
import noNativeSmoothScroll from "./rules/no-native-smooth-scroll.js";
import noShowAccessorPropLeak from "./rules/no-show-accessor-prop-leak.js";
import sourceContracts from "./rules/source-contracts.js";

export default {
  rules: {
    "no-computation-in-jsx-callback": noComputationInJsxCallback,
    "no-http-error-status-branching": noHttpErrorStatusBranching,
    "no-keyed-settings-draft": noKeyedSettingsDraft,
    "no-native-smooth-scroll": noNativeSmoothScroll,
    "no-show-accessor-prop-leak": noShowAccessorPropLeak,
    "source-contracts": sourceContracts,
  },
};
