use super::Result;
use mlx_rs::{Array, Dtype, Stream};
use mlx_sys as sys;

// Preserve the cube and activation rounding before the final product.
const SOURCE: &std::ffi::CStr = c"
    uint i = thread_position_in_grid.x;
    uint width = gate_shape[2];
    uint row = i / width;
    uint col = i % width;
    float x = float(gate[row * gate_strides[1] + col * gate_strides[2]]);
    half cube = half(metal::pow(x, 3.0f));
    float inside = x + 0.044715f * float(cube);
    float activation = 0.5f * x * (1.0f + metal::precise::tanh(metal::sqrt(0.6366197466850281f) * inside));
    half rounded = half(activation);
    out[i] = half(float(rounded) * float(up[row * up_strides[1] + col * up_strides[2]]));
";

pub struct Activation(sys::mlx_fast_metal_kernel);

fn checked(status: i32, operation: &str) -> Result<()> {
    if status != 0 {
        return Err(format!("Metal activation {operation} failed (MLX status {status})").into());
    }
    Ok(())
}

impl Activation {
    pub fn new() -> Result<Self> {
        Self::create(c"e4b_gelu_product", SOURCE)
    }

    fn create(name: &std::ffi::CStr, source: &std::ffi::CStr) -> Result<Self> {
        let mut names = [c"gate".as_ptr(), c"up".as_ptr()];
        let input_names = unsafe { sys::mlx_vector_string_new_data(names.as_mut_ptr(), 2) };
        let output_names = unsafe { sys::mlx_vector_string_new_value(c"out".as_ptr()) };
        if input_names.ctx.is_null() || output_names.ctx.is_null() {
            unsafe {
                sys::mlx_vector_string_free(input_names);
                sys::mlx_vector_string_free(output_names);
            }
            return Err("Cannot allocate Metal activation argument names".into());
        }
        let kernel = unsafe {
            sys::mlx_fast_metal_kernel_new(
                name.as_ptr(),
                input_names,
                output_names,
                source.as_ptr(),
                c"".as_ptr(),
                false,
                false,
            )
        };
        unsafe {
            sys::mlx_vector_string_free(input_names);
            sys::mlx_vector_string_free(output_names);
        }
        if kernel.ctx.is_null() {
            return Err("Metal activation kernel is unavailable".into());
        }
        Ok(Self(kernel))
    }

    pub fn product(&self, gate: &Array, up: &Array) -> Result<Array> {
        let shape = gate.shape();
        if shape.len() != 3
            || shape[0] != 1
            || !(1..=256).contains(&shape[1])
            || ![256, 10240].contains(&shape[2])
            || up.shape() != shape
            || gate.dtype() != Dtype::Float16
            || up.dtype() != Dtype::Float16
        {
            return Err("E4B activation inputs exceed their bounds".into());
        }
        let mut call = ProductDispatch::new(gate, up)?;
        unsafe {
            checked(
                sys::mlx_fast_metal_kernel_config_add_output_arg(
                    call.config,
                    shape.as_ptr(),
                    shape.len(),
                    sys::mlx_dtype__MLX_FLOAT16,
                ),
                "output configuration",
            )?;
            checked(
                sys::mlx_fast_metal_kernel_config_set_grid(call.config, gate.size() as i32, 1, 1),
                "grid configuration",
            )?;
            checked(
                sys::mlx_fast_metal_kernel_config_set_thread_group(call.config, 256, 1, 1),
                "threadgroup configuration",
            )?;
            checked(
                sys::mlx_fast_metal_kernel_apply(
                    &mut call.outputs,
                    self.0,
                    call.inputs,
                    call.config,
                    Stream::gpu().as_ptr(),
                ),
                "dispatch construction",
            )?;
            let mut output = sys::mlx_array_new();
            if output.ctx.is_null() {
                return Err("Cannot allocate Metal activation result handle".into());
            }
            let status = sys::mlx_vector_array_get(&mut output, call.outputs, 0);
            let output = Array::from_ptr(output);
            checked(status, "result extraction")?;
            Ok(output)
        }
    }
}

impl Drop for Activation {
    fn drop(&mut self) {
        unsafe { sys::mlx_fast_metal_kernel_free(self.0) };
    }
}

struct ProductDispatch {
    config: sys::mlx_fast_metal_kernel_config,
    inputs: sys::mlx_vector_array,
    outputs: sys::mlx_vector_array,
}

impl ProductDispatch {
    fn new(gate: &Array, up: &Array) -> Result<Self> {
        let inputs = [gate.as_ptr(), up.as_ptr()];
        let call = unsafe {
            Self {
                config: sys::mlx_fast_metal_kernel_config_new(),
                inputs: sys::mlx_vector_array_new_data(inputs.as_ptr(), inputs.len()),
                outputs: sys::mlx_vector_array_new(),
            }
        };
        call.validate()?;
        Ok(call)
    }

    fn validate(&self) -> Result<()> {
        if self.config.ctx.is_null() || self.inputs.ctx.is_null() || self.outputs.ctx.is_null() {
            return Err("Cannot allocate Metal activation dispatch handles".into());
        }
        Ok(())
    }
}

impl Drop for ProductDispatch {
    fn drop(&mut self) {
        unsafe {
            sys::mlx_fast_metal_kernel_config_free(self.config);
            sys::mlx_vector_array_free(self.inputs);
            sys::mlx_vector_array_free(self.outputs);
        }
    }
}

#[cfg(test)]
#[path = "activation_tests.rs"]
mod tests;
